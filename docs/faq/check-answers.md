# How do I test that Meru still answers well after a change?

`meru check` asks a fixed set of your questions, grades each answer against
what you expect, and prints PASS or FAIL. Run it after an update, a model
switch or a config change, and compare with the last run.

1. **Start from the example:**

   ```sh
   cp docs/examples/checks.example.jsonl ~/.meru/checks.jsonl
   ```

2. **Write your questions**, one JSON object per line:

   ```json
   {"id": "direct-capital", "category": "direct", "question": "What is the capital of Australia?", "want": {"route": ["direct"], "answer_any": ["Canberra"]}}
   {"id": "garden-sow", "category": "files", "question": "When does the garden project sow tomatoes?", "want": {"sources_any": ["garden"], "answer_any": ["12 April"]}}
   ```

   `want` can check the route, the tools that ran, words the answer must or
   must not hold, the files the search found, and a time limit.
   [running.md](../running.md#check-answers-on-your-own-files) lists every
   field.

3. **Run it:**

   ```sh
   meru check                         # every question
   meru check --only files,web-go     # some categories or ids
   meru check --save                  # also keep the results
   ```

   ```text
   PASS  direct-capital  direct  direct   1.0s  -
   FAIL  garden-sow      files   search   8.3s  grep
         answer lacks all of: 12 April

   1 of 2 passed in 9s
   ```

`--save` appends the results to `~/.meru/checks-results/<date>.jsonl`, with each
whole answer, so you can compare runs.

`meru check` declines every tool call that would ask first, since nobody
watches it. A question that needs such a tool fails.
