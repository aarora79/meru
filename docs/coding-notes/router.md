# router

**Code:** `internal/router/` (`router.go`, `prompt.go`, `probs.go`; the scoring
harness in `eval_test.go` and `eval_integration_test.go`)
**Milestone:** v0.1
**Architecture:** [Routing](../../ARCHITECTURE.md#routing); full design in [docs/fast-router.md](../fast-router.md)

## What it does

Each turn starts with a choice of route: answer from the model alone (`direct`),
search the user's files first (`search`), call tools (`tools`), or both
(`search+tools`). The router makes that choice. The agent loop calls
`router.Decide` once per turn and gets back a route, how sure the router was, and
an outcome.

The router doesn't ask the model to write its answer. It asks for one token and
reads how likely the model thought each of the letters A, B, C and D was.

## The picture

```mermaid
flowchart LR
    T["Turn<br/>question, history, folders"] --> P["buildMessages<br/>options A–D, folders, examples,<br/>then the turn, ends 'Answer: '"]
    P --> G["engine.Generate<br/>MaxTokens 1, LogProbs, TopLogProbs 20"]
    G --> L["log probabilities<br/>at position 0"]
    L --> F["probs<br/>keep A–D, exp(lp / T), normalise"]
    F --> C{"≥ 2 letters?<br/>winner ≥ min_confidence?"}
    C -- yes --> R["route, outcome ok"]
    C -- "too few letters" --> D["fallback, degraded"]
    C -- "winner too weak" --> W["fallback, low_confidence"]
```

The picture shows the default rule, `top`. With `decision = "marginal"` the
diamond changes: after the two-letter check, `marginal` adds P(B) + P(D) and
P(C) + P(D), compares each sum with its threshold, and returns the route with
outcome `ok`. It never gives `low_confidence`.

## Walk through the code

### prompt.go

The four options and their letters live in one array next to the prompt builder,
so the prompt and the letter table can't drift apart:

```go
var options = [...]option{
    {"A", RouteDirect, "General knowledge, chit-chat, maths, coding or writing help. ..."},
    {"B", RouteSearch, "Look in the user's own saved notes, documents, code repos or past chats. ..."},
    ...
}
```

A second array, `examples`, holds two short questions per route. Each example
names its route, and `letterFor` turns the route into its letter, so changing a
letter can't leave an example pointing at the wrong option.

`buildMessages` writes the fixed part first (the options, then the examples),
then the history newest first, then the question, and ends with `Answer: ` so
the model's next token is a letter. The fixed part comes first for two reasons
the labelled set measured: accuracy rose, and Ollama can reuse its work on a
prompt opening it saw on the last turn, so the longer prompt still runs faster.

When `Turn.Folders` holds the `[index] folders`, B's line gains one more
sentence:

```text
The user's files are in ~/notes, ~/repos/meru; questions about projects kept there, by name, are B.
```

Without it the model can't tell that "meru" in "what database does meru use"
names the user's own project, and it answered `direct`. `merud` fills
`Turn.Folders` from config, in `routerAdapter` in `cmd/merud/main.go`. Config
changes only when `merud` restarts, so the line stays the same from turn to turn
and counts as part of the fixed part.

`routeForLetter` trims spaces and ignores case. A tokenizer may emit `A`, ` A` or
`a` for the same answer; all three map to `direct`. `AB`, `Alpha` and `1` map to
nothing.

### probs.go

`probs` turns log probabilities into a distribution over routes:

```go
for _, alt := range pos.Top {
    r, ok := routeForLetter(alt.Token)
    if !ok {
        continue
    }
    raw[r] += math.Exp(alt.LogProb / temp)
}
// ... then divide each by the sum
```

A log probability is the natural logarithm of a probability, so `math.Exp` turns
it back. Dividing by a temperature above 1 flattens the distribution, for a model
that claims more certainty than it has. The fitted default is 1.25. The
temperature never changes which route wins. Tokens that map to the same route,
such as `A` and ` A`, add up.

`raw` is a *map*: Go's hash table, here from `Route` to `float64`. Reading a key
that isn't there gives 0. Go walks a map in random order, so `best` walks the
fixed `options` array instead. On a tie, the earlier letter wins, and the same
input always gives the same route.

### router.go

`Decide` starts a `meru.route` span. `ask` sends the routing prompt for one token
inside a `gen_ai.chat` span (tier `fast`), and `Decide` hands the reply to
`decide`:

```go
if len(p) < 2 {
    return Decision{Route: cfg.Fallback, Outcome: OutcomeDegraded, Probs: p}
}
win, conf := best(p)
if conf < cfg.MinConfidence {
    return Decision{Route: cfg.Fallback, Confidence: conf, Outcome: OutcomeLowConfidence, Probs: p}
}
```

Fewer than two letters means the model didn't weigh the options, so the numbers
mean nothing. A winner below `min_confidence` means the model couldn't decide.
Both take the fallback, `search+tools`, which is the one route that can't fail a
turn for lack of context.

Between those two checks sits the second rule. `Config.Rule` is a `Rule`, a named
string type like `Route`, and its zero value `""` means `RuleTop`, so a `Config`
built by hand in a test keeps the old behaviour:

```go
if cfg.Rule == RuleMarginal {
    r, conf := marginal(p, cfg.SearchThreshold, cfg.ToolsThreshold)
    return Decision{Route: r, Confidence: conf, Outcome: OutcomeOK, Probs: p}
}
```

`marginal`, in `probs.go`, adds P(B) + P(D) as the chance the turn needs a search
and P(C) + P(D) as the chance it needs tools, then asks `routeFor` for the route
that offers each half whose sum reached its threshold. Its confidence is the
weaker of the two answers: the sum for a yes, one minus the sum for a no. `min`
is a Go built-in since 1.21, like `max`.

`Decide` returns an error only when the model call fails or the config is
invalid. An unsure model isn't an error. `Decide` then records
`meru.route.decisions` through `obs.RecordRoute` and sets `meru.route.decision`,
`meru.route.confidence` and `meru.route.outcome` on the span, plus one number per
route: `meru.route.p.direct`, `meru.route.p.search`, `meru.route.p.tools` and
`meru.route.p.search+tools`. They are numbers about the router, never the
question's text, so they go on every span. The metric keeps only route and
outcome, because a probability would make a new series for every value.

At debug level `Decide` writes one line to `cfg.Log` with the route, the
confidence, the outcome, the whole distribution and the time taken:

```text
msg=route route=tools confidence=0.958 outcome=ok probs="direct=0.005 search=0.001 tools=0.958 search+tools=0.036" model=… ms=10 trace_id=…
```

`Config.Log` is optional. `ConfigFrom` leaves it nil, and `logger` turns nil into
a logger that writes nothing, so tests need no logger. `merud` sets it to its own.

`ConfigFrom` builds a `Config` from the `[router]` table in `config.toml` and the
fast model's name, and checks every value: `top_logprobs` 1 to 20, `temperature`
above 0, `min_confidence` 0 to 1, `fallback` one of the four routes, `decision`
`top` or `marginal`, and `search_threshold` and `tools_threshold` 0 to 1.

### eval_test.go and eval_integration_test.go

These two files score the router; neither ships in `merud`.

`eval_test.go` has no build tag, so a plain `go test` checks its arithmetic
against fixtures. `loadLabelled` reads `testdata/routes.jsonl`, one JSON object
per line:

```json
{"q": "what did I change in the blog repo this week?", "route": "search", "why": "the owner's own repository history"}
```

A follow-up row adds `"history": [{"q": "...", "a": "..."}]`, and `turn` turns
it into user and assistant messages. `split` puts the 3rd, 6th and 9th row of
every ten with the same route into the held-out set, so each route keeps its
share and every run gets the same split. `score` replays `decide` over saved
completions and fills a `report`: accuracy, per-route precision and recall, the
confusion matrix, `ece` (expected calibration error over ten bins), the fallback
rate, the missed rate and latency percentiles. `fitTemperature` runs `score`
over a grid of temperatures and keeps the one with the lowest ECE.

A row may carry `"tag": "connected"`, which marks the questions about mail, the
calendar, Drive, the Obsidian vault, a local command or the clock. The report
scores those rows as a set of their own.

To compare the two decision rules, the file defines `rule`, a function type:
`type rule func(comp engine.Completion) Route`. In Go a function is a value, so
`topRule(cfg)` and `marginalRule(cfg, 0.3, 0.25)` each return a closure, a
function that keeps the `cfg` it was built with. `scoreRule` runs one rule and a
base rule over the same samples and fills a `ruleReport`: exact accuracy, the
missed rate, extra searches and extra tools the label doesn't need, and how many
rows differ from the base. `rankGrid` sorts the threshold pairs by the fit set
alone, with `slices.SortFunc` and a comparison that returns a negative number
when the first pair should come first.

`eval_integration_test.go` has the `integration` tag. It wraps the real engine
in a `recorder`, a struct that embeds `engine.Engine` and overrides `Generate`
to keep the last completion. Each labelled question goes through `Decide`, the
real code path, and the recorder hands the raw log probabilities to `score`.
One pass over the model then covers every temperature and threshold.

## Go ideas used here

- **Maps** — `map[Route]float64`; a missing key reads as 0.
- **Named string types** — `Route`, `Outcome` and `Rule` are strings the
  compiler keeps apart from plain strings.
- **Function values and closures** — the harness's `rule` type is a function;
  `topRule` returns one that remembers its `cfg`.
- **Interfaces** — `Decide` takes an `engine.Engine`, so tests pass a fake. More
  in [go-basics/interfaces.md](go-basics/interfaces.md).
- **Embedding an interface in a struct** — the test's `fakeEngine` embeds
  `engine.Engine` to get the three methods it doesn't need, and writes only
  `Generate`.
- **Build tags** — `//go:build integration` at the top of `integration_test.go`
  and `eval_integration_test.go` keeps the real-model tests out of a plain
  `go test`.

## Try it

```sh
go test -race ./internal/router/
go test -tags integration -v -run Integration ./internal/router/
make router-eval
```

The last two need Ollama running with the fast model pulled. The second routes
seven questions and prints each distribution. `make router-eval` scores all 153
labelled questions and prints the report, a temperature sweep, a
`min_confidence` sweep, and both decision rules side by side with a sweep of the
`marginal` thresholds. It gives every row the folders in `evalFolders`
(`~/notes`, `~/repos/meru`, `~/repos/blog`). On the development machine
(Ollama 0.34, MiniCPM5-2B at Q4_K_M) the router picked the labelled route for 32
of 40 held-out questions, at about 28 ms per warm decision. Without the folder
line it picked 30 of 40. See
[Calibration](../fast-router.md#calibration) and the notes at the end of that
page.

## Why it's built this way

Asking a 2B model to write `{"route": "search"}` fails three ways: broken JSON, an
invented fifth route, or a confident wrong answer with no sign of doubt. Reading
one token's probabilities removes the first two, because the answer comes from a
fixed set of letters. It also turns the third into a number the router can act on
and the dashboard can count. The model decodes one token, so the whole decision
costs one prompt evaluation.
