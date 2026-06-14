# ah-mcp — Albert Heijn MCP Server

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

A [Model Context Protocol](https://modelcontextprotocol.io) server for the Albert Heijn (🇳🇱) supermarket API. Works with any MCP-compatible client.

## What you can do

Things the AH app and website can't do — but your AI assistant can:

**Smart shopping**
> *"Check my shopping list and add everything that's currently on bonus to my cart"*

> *"I want to make spaghetti bolognese for 4 people — find the ingredients at AH and add them to my list"*

> *"Find me a healthy snack that's on bonus and costs less than €2"*

**Know your habits**
> *"What do I order most often? Show me the top 10 and check which ones are on bonus this week"*

> *"What did I spend on groceries last month based on my kassabonnen?"*

> *"I usually buy melk, kaas, and brood — am I missing any of them in my current cart?"*

**Last-minute deals**
> *"What vandaag-af items are available near postal code 1234AB? Anything worth getting?"*

> *"Are there any bonus deals on dairy products this week?"*

**Cart management**
> *"Clear my cart and rebuild it from my shopping list"*

> *"I'm over budget — which items in my cart are not on bonus and could be swapped for cheaper alternatives?"*

---

## What this is

**ah-mcp** exposes the Albert Heijn mobile API as MCP tools so your AI assistant can:

- Search products, check bonus offers, and drill into promotion groups
- Browse last-chance / vandaag-af clearance items (store-specific)
- Manage your online shopping cart (view, add, update, remove, clear)
- View order history, order details, and frequently bought items
- Read and update your shopping list and named favourite lists
- Move your shopping list directly to your online order
- View in-store receipts (kassabonnen) with full item details
- Fetch your member profile and find nearby stores

Authentication is handled entirely through a reverse-proxy OAuth flow — no tokens are ever stored in the project directory.

## Compatibility

| Client | Transport | Status |
|---|---|---|
| Claude Desktop | stdio | ✅ tested |
| Claude Desktop | Streamable HTTP | ✅ tested |
| Claude.ai (web) | Streamable HTTP | ✅ tested |

## Quick start (pre-built binary)

1. Download the latest binary from Releases.
2. Run it:
   ```bash
   ./ah-mcp --transport stdio              # local client — browser opens automatically on login
   ./ah-mcp --transport sse                # local SSE — browser opens automatically on login
   ./ah-mcp --transport sse --remote       # remote SSE server — login returns a URL instead
   ./ah-mcp --transport streamable-http    # remote Streamable HTTP — more stable through proxies
   ```
3. Ask your AI assistant to call **`ah_login`**. In local mode the browser opens automatically; in remote mode the assistant returns a URL for you to open.

## Build from source

```bash
git clone https://github.com/mrserzhan/ah-mcp
cd ah-mcp
go build -o ah-mcp .
```

Requires Go 1.23+.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `AH_CALLBACK_HOST` | `http://localhost:9876` | Base URL for the OAuth proxy. Users open this URL in their browser during login. Override to your server's public URL for remote deployments. |
| `AH_CALLBACK_PORT` | `9876` | Port the temporary OAuth reverse-proxy server listens on. |
| `AH_MCP_PORT` | `3000` | Port for the MCP HTTP server (`--transport sse` or `--transport streamable-http`). |
| `AH_MCP_BASE_URL` | `http://localhost:3000` | Public base URL advertised to MCP clients. **Must be set for remote deployments** — otherwise clients receive a `localhost` URL they cannot reach. Example: `https://myserver.example.com` |
| `AH_TOKENS_PATH` | `~/.config/ah-mcp/tokens.json` | Override the XDG token storage path. Directory is created automatically (mode `0700`). File is written with mode `0600`. |
| `AH_REMOTE` | `false` | Set to `true` to enable remote mode (same as `--remote` flag). Disables automatic browser opening on login. |
| `AH_MCP_TOKEN` | *(unset)* | Secret token required to access the SSE server. When set, all requests must supply it via `Authorization: Bearer <token>` header or `?token=<token>` query parameter. Strongly recommended for public deployments. |

Copy `.env.example` to `.env` and uncomment lines you want to change.

## Token storage

Tokens are stored in the XDG-compliant config directory:

| Platform | Default path |
|---|---|
| Linux | `~/.config/ah-mcp/tokens.json` |
| macOS | `~/Library/Application Support/ah-mcp/tokens.json` |
| Windows | `%AppData%\ah-mcp\tokens.json` |

The directory is created with mode `0700` and the file with mode `0600` (owner read/write only). Tokens are refreshed automatically before every API call — you only need to run `ah_login` once.

Override with `AH_TOKENS_PATH` if needed.

## First login

Just call the **`ah_login`** tool from your AI assistant.

**Local mode** (default — browser opens and the call blocks until login completes):
```
User: log in to ah
Assistant: calls ah_login  ← browser opens automatically
→ (you complete login in browser)
→ "Login successful! Connected as Jan Jansen."  ← single call, no confirmation needed
```

**Remote mode** (`--remote` flag or `AH_REMOTE=true` — URL returned for manual opening):
```
User: log in to ah
Assistant: calls ah_login
→ "Please open this URL in your browser to log in to Albert Heijn:
   https://ah-mcp.example.com/login?..."
→ (complete login in browser)
→ (call ah_login again)
→ "Login successful! Connected as Jan Jansen."
```

No configuration needed for local use.

## Client setup guides

### Claude.ai (web) / Claude Desktop — Streamable HTTP (recommended for remote)

Streamable HTTP uses regular HTTP requests instead of a persistent SSE connection — more stable through proxies and cloud infrastructure.

1. Start the server with `--remote` on your remote machine:
   ```bash
   ./ah-mcp --transport streamable-http --remote
   ```
2. Open Claude → Settings → Connections → Add custom MCP server.
3. Paste the URL: `https://your-server/mcp?token=your-secret-token`

### Claude.ai (web) — SSE (legacy)

1. Start the server: `./ah-mcp --transport sse` (exposes port 3000).
2. Open Claude.ai → Settings → Connections → Add MCP server.
3. Paste the SSE URL: `http://your-server:3000/sse`

### Claude Desktop — SSE or stdio

**SSE** (remote server — run with `--remote` on the server side):

```json
{
  "mcpServers": {
    "ah": {
      "url": "http://your-server:3000/sse"
    }
  }
}
```

**stdio** (local binary):

```json
{
  "mcpServers": {
    "ah": {
      "command": "/path/to/ah-mcp",
      "args": ["--transport", "stdio"]
    }
  }
}
```

### Windsurf / Cursor — stdio

Add to your MCP config (usually `~/.codeium/windsurf/mcp_config.json` or `~/.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "ah": {
      "command": "/path/to/ah-mcp",
      "args": ["--transport", "stdio"]
    }
  }
}
```

## Remote server deployment

### systemd setup

1. Create a dedicated user:
   ```bash
   sudo useradd -r -m -d /home/ah-mcp -s /sbin/nologin ah-mcp
   ```
2. Copy the binary:
   ```bash
   sudo cp ah-mcp /usr/local/bin/ah-mcp
   sudo chmod 755 /usr/local/bin/ah-mcp
   ```
3. Create `/home/ah-mcp/.env`:
   ```env
   AH_CALLBACK_HOST=https://ah-mcp.example.com
   AH_MCP_BASE_URL=https://ah-mcp.example.com
   AH_MCP_PORT=3000
   AH_REMOTE=true
   AH_MCP_TOKEN=your-secret-token-here
   ```
4. Install and start the service:
   ```bash
   sudo cp ah-mcp.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now ah-mcp
   ```
5. Put a reverse proxy (nginx, Caddy) in front to handle TLS.

Tokens are stored automatically at `/home/ah-mcp/.config/ah-mcp/tokens.json` — no `AH_TOKENS_PATH` override needed.

## Available tools

### Authentication

| Tool | Description |
|---|---|
| `ah_login` | Log in via browser OAuth. First call returns a URL; call again after completing login. |
| `ah_logout` | Delete stored tokens to log out or switch accounts. |

### Products

| Tool | Description |
|---|---|
| `ah_search_products` | Search products by keyword (Dutch terms preferred). |
| `ah_search_products_filtered` | Search with optional `bonus=true` filter for on-sale items only. |
| `ah_get_product` | Full detail for one product by ID. Add `include_nutritional_info=true` for calories, fat, protein, etc. |
| `ah_get_bonus_offers` | Bonus/promotional offers. Defaults to this week; set `period=next` for [next week's bonus](https://www.ah.nl/bonus/volgende-week). Optional keyword filter. |
| `ah_get_bonus_group_products` | All products in a specific bonus deal group (e.g. "2+1 gratis"). Use `segment_id` from `ah_get_bonus_offers`. |
| `ah_get_last_chance_items` | Vandaag-af / clearance items from a specific store. |
| `ah_search_stores` | Find AH stores near a postal code (or your registered address). |

### Shopping cart (online order)

| Tool | Description |
|---|---|
| `ah_get_cart` | View current online cart: items, quantities, total price. |
| `ah_get_cart_summary` | Cart totals only: item count, price, discount, delivery cost. |
| `ah_update_cart_item` | Set quantity for a product in the cart (0 removes it). |
| `ah_remove_from_cart` | Remove a single product from the cart. |
| `ah_clear_cart` | Remove all items from the cart. Requires `confirm=yes`. |

### Order history & editing

| Tool | Description |
|---|---|
| `ah_get_order_history` | Upcoming delivery orders with status and modifiable flag. |
| `ah_get_past_orders` | Past/delivered orders. |
| `ah_get_order_details` | Full item list for a specific past or upcoming order. |
| `ah_get_frequent_items` | Products you order most often, ranked by frequency. |
| `ah_reopen_order` | Unlock a submitted order for editing (before closing time). ⚠️ unconfirmed |
| `ah_update_order_items` | Add/change/remove items in a reopened order. ⚠️ unconfirmed |
| `ah_revert_order` | Resubmit a reopened order. **Always call this after `ah_reopen_order`.** ⚠️ unconfirmed |

### Shopping list

| Tool | Description |
|---|---|
| `ah_get_shopping_list` | View your shopping list with item names and IDs. |
| `ah_add_to_shopping_list` | Add products by ID and quantity. |
| `ah_add_free_text_to_shopping_list` | Add a free-text reminder (no product ID needed). |
| `ah_remove_from_shopping_list` | Remove items by product ID or free-text name. |
| `ah_check_shopping_list_item` | Tick or untick an item on the list. ⚠️ broken (listItemId=0 in API) |
| `ah_clear_shopping_list` | Remove all items from the list. Requires `confirm=yes`. |
| `ah_shopping_list_to_order` | Move all unchecked product items from your list to the cart. |
| `ah_get_favorite_lists` | List all named favourite lists with IDs. |
| `ah_add_to_favorite_list` | Add products to a named favourite list. |
| `ah_remove_from_favorite_list` | Remove products from a named favourite list. |

### Receipts

| Tool | Description |
|---|---|
| `ah_get_receipts` | List recent in-store receipts (kassabonnen) with dates and totals. |
| `ah_get_receipt_details` | Full item breakdown, discounts, and payment method for one receipt. |

### Member

| Tool | Description |
|---|---|
| `ah_get_member_profile` | Your name, email, and bonus card number. |

## Troubleshooting

**Port conflict on 9876 or 3000**
Change via `AH_CALLBACK_PORT` or `AH_MCP_PORT` in your `.env`.

**Login timeout after 5 minutes**
The OAuth flow timed out. Call `ah_login` again and complete the browser flow faster.

**Token issues / expired session**
Call `ah_logout` then `ah_login`. Or delete `tokens.json` manually:
- Linux: `rm ~/.config/ah-mcp/tokens.json`
- macOS: `rm ~/Library/Application\ Support/ah-mcp/tokens.json`
- Windows: `del %AppData%\ah-mcp\tokens.json`

**Last-chance items require a store**
`ah_get_last_chance_items` needs a store ID or postal code — bargain items are store-specific. Provide `store_id` or `postal_code` as a parameter.

**"Not logged in" error**
Run `ah_login` first. In local mode the browser opens automatically; in remote mode (`--remote` / `AH_REMOTE=true`) open the URL the assistant returns.

## Acknowledgements

**ah-mcp** is built on top of [**appie-go**](https://github.com/gwillem/appie-go) — a Go client library for the Albert Heijn mobile API by [@gwillem](https://github.com/gwillem). It provides the authenticated HTTP client, all API call implementations (product search, bonus offers, orders, shopping lists, member profile, bargain items), and the OAuth token format. This project uses it as a library dependency without modification.

The OAuth reverse-proxy login flow in `auth.go` is inspired by the approach in appie-go's `login.go`, adapted to return a URL string rather than open a browser — making it safe for server-side MCP use.
