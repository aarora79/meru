# How do I connect Robinhood or Yahoo Finance?

Two MCP servers give Meru market and account data: Robinhood's, which runs on
the internet and signs you in, and Yahoo Finance's, which runs on your machine
and needs no account. Both entries below allow only tools that read. Meru never
places, changes or cancels an order with them; leave every such tool out of
`allow`.

## Robinhood

Robinhood's server is `https://agent.robinhood.com/mcp/trading`. It registers Meru
by itself, so there's no app to create and no client ID to copy.

1. **Add the entry** to `~/.meru/config.toml`:

   ```toml
   [[mcp.servers]]
   name   = "robinhood"
   url    = "https://agent.robinhood.com/mcp/trading"
   remote = true
   auth   = "oauth"
   allow  = [
     "get_accounts", "get_portfolio", "get_equity_positions", "get_option_positions",
     "get_equity_quotes", "get_equity_fundamentals", "get_equity_orders",
     "get_option_orders", "get_option_quotes", "get_realized_pnl", "get_equity_historicals",
   ]
   ```

2. **Restart `merud`** (`launchctl kickstart -k gui/$(id -u)/com.meru.merud`, or
   `systemctl --user restart merud` on Linux). `meru mcp` shows `robinhood` as
   `sign_in`.

3. **Sign in** from **Settings → Connections → robinhood → Sign in** in the
   desktop app, `/signin robinhood` in `meru chat`, or `meru mcp login robinhood`.
   Approve Meru on Robinhood's page. Meru keeps the token in
   `~/.meru/oauth/robinhood.json`, readable only by you, and saves each refreshed
   one.

4. **Check** with `meru tools`, then ask "What accounts do I have on Robinhood?".

The server offers tools that place, change and cancel orders, such as
`place_equity_order`. Keep them out of `allow`. A tool left out never reaches
the model.

## Yahoo Finance

[mcp-yahoo-finance](https://github.com/maxscheijen/mcp-yahoo-finance) runs on your
machine through `uvx` and reads public market data: quotes, price history,
company financials, dividends, options and news. It needs no account and no
key.

1. **Install `uv`** if `command -v uvx` prints nothing: `brew install uv`. Use the
   full path that `command -v uvx` prints in the entry, since `merud` under
   `launchd` doesn't search `/opt/homebrew/bin`.

2. **Add the entry.** Every tool only reads, so none asks first:

   ```toml
   [[mcp.servers]]
   name    = "yahoo-finance"
   command = "/opt/homebrew/bin/uvx"
   args    = ["mcp-yahoo-finance"]
   allow   = [
     "get_current_stock_price", "get_rich_quote", "get_stock_price_by_date",
     "get_stock_price_date_range", "get_historical_stock_prices", "get_symbol_comparison",
     "get_performance_analysis", "get_company_overview", "get_income_statement",
     "get_cashflow", "get_earning_dates", "get_earnings_analytics", "get_recommendations",
     "get_dividends", "get_upcoming_dividends", "get_capital_gains", "get_stock_splits",
     "get_option_expiration_dates", "get_option_chain", "get_option_summary", "get_news",
   ]
   ```

   `meru mcp add stdio yahoo-finance -- /opt/homebrew/bin/uvx mcp-yahoo-finance`
   writes the same entry after it lists the tools, with each one set to ask first.

3. **Restart `merud`.** The first start downloads the server, which takes a
   minute. `meru mcp` then shows `yahoo-finance` as connected, offering 21 tools.

## When it goes wrong

| What you see | What to do |
| --- | --- |
| `robinhood` stays `sign_in` after you approve | Sign in again; Meru waits up to ten minutes for the browser. |
| "the server refused the sign-in" | Sign in again. If it keeps failing, remove `~/.meru/oauth/robinhood.json` and restart `merud`. |
| `yahoo-finance` shows `exec: "uvx": executable file not found` | Put the full path from `command -v uvx` in `command`. |
| `meru tools` warns that a name isn't offered | The server renamed a tool. Take the new name from `meru tools`. |

[oauth-mcp-server.md](oauth-mcp-server.md) covers signing in to other servers on
the internet, and [connect-mcp-server.md](connect-mcp-server.md) covers servers
that run on your machine.
