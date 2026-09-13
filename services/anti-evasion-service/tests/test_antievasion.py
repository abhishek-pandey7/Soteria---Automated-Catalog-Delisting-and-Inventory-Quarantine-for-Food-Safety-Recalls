import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from antievasion.judge import Judge, decide  # noqa: E402
from antievasion.match import candidates, date_keys, score  # noqa: E402
from antievasion.model import Candidate, Listing, Recall, Verdict, gtin14  # noqa: E402
from antievasion.pipeline import Pipeline, Watchlist, envelope  # noqa: E402
from antievasion.sources import load_listings  # noqa: E402

HERE = Path(__file__).resolve().parent
FIX = HERE.parent / "fixtures"
CONTRACTS = HERE.parents[2] / "contracts" / "events"


def watchlist() -> Watchlist:
    w = Watchlist(":memory:")
    assert w.load_yaml(str(FIX / "watchlist.yaml")) == 3
    return w


def listings() -> list[Listing]:
    return load_listings(str(FIX / "listings" / "liquidation_2026-09-12.json"))


class ScriptedJudge(Judge):
    """Answers per listing id; unknown ids fall back to rules."""

    name = "scripted"

    def __init__(self, answers: dict[str, Verdict]):
        self.answers = answers
        self.seen: list[str] = []

    def judge(self, c: Candidate) -> Verdict:
        self.seen.append(c.listing.listing_id)
        return self.answers.get(c.listing.listing_id) or super().judge(c)


class Collect:
    def __init__(self):
        self.events = []
        self.fail = False

    def publish(self, env):
        if self.fail:
            raise RuntimeError("broker down")
        self.events.append(env)


# ---- matching ----------------------------------------------------------------

def test_gtin14_and_dates():
    assert gtin14("194346207961") == "00194346207961"
    assert gtin14("41415-12153") == ""
    assert date_keys("exp January 28, 2027 and 31 JUL 2027 and 1/28/27 and 2027-01-28") == {"2027-01-28", "2027-07-31"}


def test_prefilter_signals():
    recalls = {r.incident_id: r for r in watchlist().all()}
    by = {l.listing_id: l for l in listings()}
    pb = recalls["inc-fda_enforcement-h-1273-2026"]

    c = score(pb, by["liq-1001"])
    assert c.lot_hit == "LB028ACP04" and not c.gtin_hit and c.score >= 0.75
    c = score(pb, by["liq-1002"])
    assert c.gtin_hit and c.score >= 0.8
    c = score(pb, by["liq-1009"])
    assert c.date_hit == "January 28, 2027" and c.score >= 0.65
    c = score(pb, by["liq-1003"])  # almond butter: same brand, different product
    assert not c.gtin_hit and not c.lot_hit and c.score < 0.5
    c = score(recalls["inc-fda_enforcement-h-1265-2026"], by["liq-1005"])  # posted before the recall
    assert any("before recall" in s for s in c.signals) and c.score < 0.35
    c = score(recalls["inc-fda_enforcement-h-1265-2026"], by["liq-1004"])  # lot only in photo text
    assert c.lot_hit == "LLA618203" and c.date_hit == "31 JUL 2027"

    cands = candidates(list(recalls.values()), listings())
    ids = {c.listing.listing_id for c in cands}
    assert {"liq-1001", "liq-1002", "liq-1004", "liq-1006", "liq-1009"} <= ids
    assert "liq-1005" not in ids and "liq-1008" not in ids


# ---- guardrail ---------------------------------------------------------------------

def test_decide_rules():
    pb = watchlist().all()[0]
    by = {l.listing_id: l for l in listings()}
    hard = score(pb, by["liq-1001"])
    # Hard evidence flags even if the judge doubts, at reduced confidence.
    ok, conf = decide(hard, Verdict(match=False, confidence=0.2, reasons=[]))
    assert ok and conf <= 0.6
    ok, conf = decide(hard, Verdict(match=True, confidence=0.98, reasons=[]))
    assert ok and conf >= 0.9
    # Soft evidence needs a confident judge AND a plausible pre-score.
    soft = score(pb, by["liq-1010"])
    assert not decide(soft, Verdict(match=True, confidence=0.95, reasons=[]))[0]  # pre-score too low without a date hit
    date = score(pb, by["liq-1009"])
    assert decide(date, Verdict(match=True, confidence=0.95, reasons=[]))[0]
    assert not decide(date, Verdict(match=False, confidence=0.95, reasons=[]))[0]
    # Pre-recall listing never flags without a lot code.
    old = score(watchlist().all()[1], by["liq-1005"])
    assert not decide(old, Verdict(match=True, confidence=0.99, reasons=[]))[0]


# ---- pipeline -------------------------------------------------------------------------

def test_pipeline_flags_once_and_respects_judge():
    w, pub = watchlist(), Collect()
    judge = ScriptedJudge({
        "liq-1009": Verdict(match=True, confidence=0.95, reasons=["expiry matches"], model="scripted"),
        "liq-1010": Verdict(match=False, confidence=0.95, reasons=["different best-by"], model="scripted"),
        "liq-1003": Verdict(match=False, confidence=0.95, reasons=["almond, not pistachio"], model="scripted"),
    })
    pipe = Pipeline(w, judge, pub, [])
    flagged = pipe.process(listings())
    ids = sorted(e["payload"]["listing_url"].rsplit("/", 1)[1] for e in flagged)
    assert ids == ["1001", "1002", "1004", "1006", "1009"]
    by = {e["payload"]["listing_url"][-4:]: e["payload"] for e in flagged}
    assert by["1001"]["lot_code"] == "LB028ACP04" and by["1002"]["gtin"] == "194346207961"
    assert by["1004"]["incident_id"] == "inc-fda_enforcement-h-1265-2026" and by["1004"]["confidence"] >= 0.9
    assert all(e["correlation_id"] == e["payload"]["incident_id"] for e in flagged)
    # Same listings again: nothing new (already flagged), and the judge is not called again.
    seen = len(judge.seen)
    assert pipe.process(listings()) == []
    assert len(judge.seen) == seen


def test_publish_failure_does_not_mark_flagged():
    w, pub = watchlist(), Collect()
    pub.fail = True
    pipe = Pipeline(w, Judge(), pub, [])
    assert pipe.process(listings()) == []
    assert not pipe.health.broker_connected
    pub.fail = False
    assert len(pipe.process(listings())) >= 4


def test_watchlist_from_containment_event():
    w = Watchlist(":memory:")
    payload = {
        "incident_id": "inc-x", "taken_at": "2026-09-12T08:41:12Z", "hazard": "Listeria",
        "targets": [{"gtin": "041196910537", "product_title": "Sunfield Farms Chewy Granola Bars 12 ct", "lot_codes": ["8H-1132"]},
                    {"gtin": "012345678905", "product_title": "Harvest Lane bars", "lot_codes": []}],
        "results": [{"gtin": "041196910537", "status": "HELD"}, {"gtin": "012345678905", "status": "SKIPPED"}],
    }
    assert w.add_from_containment(payload, "2026-09-12T08:41:20Z") == 1
    r = w.all()[0]
    assert r.gtin == "00041196910537" and r.lot_codes == ["8H-1132"] and r.recalled_at == "2026-09-12T08:41:12Z" and r.source == "containment"


# ---- contract --------------------------------------------------------------------------

def test_events_validate_against_contracts():
    jsonschema = pytest.importorskip("jsonschema")
    payload_schema = json.loads((CONTRACTS / "evasion.flagged.v1.json").read_text(encoding="utf-8"))
    envelope_schema = json.loads((CONTRACTS / "_envelope.v1.json").read_text(encoding="utf-8"))
    pipe = Pipeline(watchlist(), Judge(), Collect(), [])
    flagged = pipe.process(listings())
    assert flagged
    for env in flagged:
        jsonschema.validate(env, envelope_schema, format_checker=jsonschema.FormatChecker())
        jsonschema.validate(env["payload"], payload_schema, format_checker=jsonschema.FormatChecker())


def test_watchlist_serves_published_flags_for_the_console():
    w, pub = watchlist(), Collect()
    pipe = Pipeline(w, Judge(), pub, [])
    emitted = pipe.process(listings())
    assert emitted

    served = w.flags()
    assert {f["flag_id"] for f in served} == {e["payload"]["flag_id"] for e in emitted}
    incident = emitted[0]["payload"]["incident_id"]
    assert all(f["incident_id"] == incident for f in w.flags(incident_id=incident))
    assert w.flags(incident_id="nope") == []
    assert len(w.flags(limit=1)) == 1
