import json
import os
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from silentdiff import events  # noqa: E402
from silentdiff.diff import diff  # noqa: E402
from silentdiff.pipeline import Pipeline  # noqa: E402
from silentdiff.sources import Row, Source, parse_shopify_products  # noqa: E402
from silentdiff.store import Store  # noqa: E402

HERE = Path(__file__).resolve().parent
FIXTURES = HERE.parent / "fixtures"
CONTRACTS = HERE.parents[2] / "contracts" / "events"


def load(name: str) -> tuple[dict[str, Row], str]:
    d = json.loads((FIXTURES / name).read_text(encoding="utf-8"))
    return {r["key"]: Row.from_dict(r) for r in d["rows"]}, d["taken_at"]


class CollectPublisher:
    def __init__(self):
        self.events = []
        self.fail = False

    def publish(self, env):
        if self.fail:
            raise RuntimeError("broker down")
        self.events.append(env)

    def close(self):
        pass


# ---- diff / scoring ------------------------------------------------------------

def test_fixture_scenarios_score_as_designed():
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")
    res = diff("shopify:lesserevil.com", before, after, b_at, a_at)
    assert res.anomaly is None
    assert len(res.appeared) == 1
    by_title = {(v.row.title, v.row.variant_title): v for v in res.vanished}

    withdrawn = by_title[("Fiery Hot Organic Popcorn", "Single / 4.6oz Bags")]
    assert withdrawn.confidence == 0.95
    assert "whole product gone" in withdrawn.reasons and "was in stock" in withdrawn.reasons

    one_size = by_title[("Himalayan Gold Organic Butter Flavor Popcorn", "Single / 14oz")]
    assert one_size.confidence == 0.75
    assert "whole product gone" not in one_size.reasons

    renamed = by_title[("Cowboy Cheddar Cheezmos", "")]
    assert renamed.confidence <= 0.35
    assert renamed.renamed_to == "variant:99000000000001"

    seasonal = by_title[("Spooky Space Balls Snack Pack", "")]
    assert "seasonal/limited wording" in seasonal.reasons
    assert seasonal.confidence < withdrawn.confidence

    # newest-first ordering by confidence
    confs = [v.confidence for v in res.vanished]
    assert confs == sorted(confs, reverse=True)


def test_shrink_anomaly_suppresses_everything():
    before, b_at = load("lesserevil_before.json")
    after = dict(list(before.items())[:10])  # 70% gone: looks like a broken fetch
    res = diff("x", before, after, b_at, "2026-09-12T07:00:00Z")
    assert res.anomaly and "shrank" in res.anomaly
    assert res.vanished == []


def test_empty_snapshots_are_anomalies():
    before, b_at = load("lesserevil_before.json")
    assert diff("x", {}, before, "", b_at).anomaly == "no previous snapshot"
    assert "empty" in diff("x", before, {}, b_at, "").anomaly


def test_identical_snapshots_emit_nothing():
    before, b_at = load("lesserevil_before.json")
    res = diff("x", before, dict(before), b_at, "2026-09-12T07:00:00Z")
    assert res.vanished == [] and res.appeared == [] and res.anomaly is None


# ---- pipeline: dedup, reappearance, broker failure -----------------------------

def test_pipeline_emits_once_and_again_only_after_reappearance():
    store, pub = Store(":memory:"), CollectPublisher()
    src = Source(name="shopify:lesserevil.com", kind="shopify", url="https://www.lesserevil.com", brand="LesserEvil")
    pipe = Pipeline(store, pub, [src], min_confidence=0.5)
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")

    assert pipe.ingest(src, list(before.values()), b_at) == []  # first snapshot: nothing to diff
    first = pipe.ingest(src, list(after.values()), a_at)
    assert len(first) == 3  # rename is below threshold
    assert {e["payload"]["brand_name"] for e in first} == {"LesserEvil"}

    # Same catalog again: the vanished items are still vanished, but already reported.
    assert pipe.ingest(src, list(after.values()), "2026-09-12T08:00:00Z") == []

    # The product comes back, then vanishes again → reported again.
    assert pipe.ingest(src, list(before.values()), "2026-09-12T09:00:00Z") == []
    again = pipe.ingest(src, list(after.values()), "2026-09-12T10:00:00Z")
    assert len(again) == 3
    assert {e["event_id"] for e in again}.isdisjoint({e["event_id"] for e in first})


def test_publish_failure_does_not_mark_emitted():
    store, pub = Store(":memory:"), CollectPublisher()
    src = Source(name="s", kind="shopify", url="")
    pipe = Pipeline(store, pub, [src])
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")
    pipe.ingest(src, list(before.values()), b_at)
    pub.fail = True
    assert pipe.ingest(src, list(after.values()), a_at) == []
    assert not pipe.health.broker_connected and not pipe.health.healthy()
    pub.fail = False
    # Broker back: the same diff (after vs after) has nothing new, so re-feed the pair.
    pipe.ingest(src, list(before.values()), "2026-09-12T09:00:00Z")
    assert len(pipe.ingest(src, list(after.values()), "2026-09-12T10:00:00Z")) == 3
    assert pipe.health.broker_connected


# ---- sources -------------------------------------------------------------------

def test_parse_shopify_products_projects_variants():
    products = [{
        "id": 1, "title": "Peanut Crunch Bars", "handle": "peanut-crunch", "vendor": "Nutty Trail", "product_type": "Bars",
        "published_at": "2026-01-01T00:00:00Z", "updated_at": "2026-02-01T00:00:00Z", "tags": ["bars"],
        "variants": [
            {"id": 11, "sku": "NT-1", "title": "Default Title", "available": True, "price": "4.99", "grams": 300},
            {"id": 12, "sku": "NT-2", "title": "12 ct", "available": False, "price": "19.99", "grams": 3600},
        ],
    }]
    rows = parse_shopify_products(Source(name="shopify:x", kind="shopify", url="https://x.com/"), products)
    assert [r.key for r in rows] == ["variant:11", "variant:12"]
    assert rows[0].variant_title == "" and rows[1].variant_title == "12 ct"
    assert rows[0].url == "https://x.com/products/peanut-crunch"
    assert rows[0].product_key == rows[1].product_key == "product:1"
    assert rows[0].available is True and rows[1].available is False
    assert Row.from_dict(rows[0].to_dict()) == rows[0]


# ---- contract --------------------------------------------------------------------

def test_events_validate_against_contracts():
    jsonschema = pytest.importorskip("jsonschema")
    payload_schema = json.loads((CONTRACTS / "catalog.sku.vanished.v1.json").read_text(encoding="utf-8"))
    envelope_schema = json.loads((CONTRACTS / "_envelope.v1.json").read_text(encoding="utf-8"))
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")
    res = diff("shopify:lesserevil.com", before, after, b_at, a_at)
    assert res.vanished
    for v in res.vanished:
        env = events.envelope(events.payload(v, upc="041196910537" if v.confidence > 0.9 else "", brand="LesserEvil"))
        jsonschema.validate(env, envelope_schema, format_checker=jsonschema.FormatChecker())
        jsonschema.validate(env["payload"], payload_schema, format_checker=jsonschema.FormatChecker())
        assert env["event_type"] == "ingestion.catalog.sku.vanished.v1"
        assert env["payload"]["sku"]  # required, falls back to the key


def test_sku_falls_back_to_key_when_listing_has_none():
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")
    res = diff("s", before, after, b_at, a_at)
    seasonal = next(v for v in res.vanished if v.row.title.startswith("Spooky"))
    assert seasonal.row.sku == ""
    assert events.payload(seasonal)["sku"] == seasonal.row.key


def test_store_serves_published_signals_for_the_console():
    store, pub = Store(":memory:"), CollectPublisher()
    src = Source(name="shopify:lesserevil.com", kind="shopify", url="https://www.lesserevil.com", brand="LesserEvil")
    pipe = Pipeline(store, pub, [src], min_confidence=0.5)
    before, b_at = load("lesserevil_before.json")
    after, a_at = load("lesserevil_after.json")
    pipe.ingest(src, list(before.values()), b_at)
    emitted = pipe.ingest(src, list(after.values()), a_at)

    served = store.signals()
    assert {s["event_id"] for s in served} == {e["event_id"] for e in emitted}
    assert all(s["source"] == src.name and s["brand_name"] == "LesserEvil" for s in served)
    assert store.signals(source="other") == []
    assert len(store.signals(limit=1)) == 1
