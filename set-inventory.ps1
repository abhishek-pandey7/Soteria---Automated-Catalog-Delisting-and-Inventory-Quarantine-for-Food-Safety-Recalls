# Set inventory quantities for all products
$env:Path += ";C:\Program Files\Go\bin"
$env:GOWORK = "off"
$env:SHOPIFY_SHOP = $env:SHOPIFY_SHOP
$env:SHOPIFY_ACCESS_TOKEN = $env:SHOPIFY_ACCESS_TOKEN

$location = "gid://shopify/Location/93301866735"
$quantity = 100

# Array of all inventory item IDs from your catalog
$items = @(
    "gid://shopify/InventoryItem/52869317099759",
    "gid://shopify/InventoryItem/52869317165295",
    "gid://shopify/InventoryItem/52869317230831",
    "gid://shopify/InventoryItem/52869317329135",
    "gid://shopify/InventoryItem/52869317361903",
    "gid://shopify/InventoryItem/52869317394671",
    "gid://shopify/InventoryItem/52869317427439",
    "gid://shopify/InventoryItem/52869317460207",
    "gid://shopify/InventoryItem/52869317492975"
)

Write-Host "Setting inventory for 9 products to $quantity units each..."

$i = 1
foreach ($item in $items) {
    Write-Host "[$i/9] Setting $item..."
    Push-Location libs/shopify
    go run ./cmd/shopctl set $item $location $quantity
    Pop-Location
    $i++
}

Write-Host "`nDone! Now run verify to check:"
Write-Host "cd tools/seedshop"
Write-Host "go run . verify"
