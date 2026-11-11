// This file holds the rule for a question about Meru itself, such as "do
// you send any tracking data to the cloud?" or "which model are you?". Such
// a turn runs about_meru before the model's first round and skips the
// search of the user's files, so the answer starts from merud's own facts.
//
// A real turn showed why. Asked "do you send any tracking data to the
// cloud", the router sent the question to search+tools, the search found
// the telemetry code of a project in the user's indexed folders, and about
// 40,000 tokens of it went into the prompt. The model read "you" as "your
// code", answered about that project, and never called about_meru, though
// the prompt says to for questions about itself.

package agent

import (
	"context"
	"regexp"
	"strings"

	"github.com/aarora79/meru/internal/builtin"
	"github.com/aarora79/meru/internal/dispatch"
	"github.com/aarora79/meru/internal/engine"
)

// selfTopic matches the words of a question about what Meru is, does or
// sends: its data and privacy, its model and version, its name. A word
// alone isn't enough; selfAddress must match too.
var selfTopic = regexp.MustCompile(`(?i)\b(` +
	`send|sends|sending|sent|share|shares|sharing|upload|uploads|` +
	`track|tracks|tracking|tracker|telemetry|analytics|metrics|phone home|spy|` +
	`collect|collects|collecting|log|logs|logging|store|stores|keep|keeps|` +
	`data|privacy|private|cloud|internet|online|leave|leaves|offline|` +
	`model|models|llm|version|update|updates|crash|report|reports|` +
	`who|name|called|built|made|work|works|run|runs|running` +
	`)\b`)

// selfSubject matches a question put to Meru itself: Meru by name, or
// "you" as the subject, as in "do you", "are you", "who are you" or "you
// send".
var selfSubject = regexp.MustCompile(`(?i)(\bmeru\b|` +
	`\b(do|does|did|will|would|could|can|are|were|is|have|has)\s+you\b|` +
	`\byou\s+(send|share|track|collect|store|keep|log|upload|use|run|phone|spy|report|work)|` +
	`\b(who|what)\s+(are|is)\s+you\b|\bare\s+you\b)`)

// selfOwner matches "your" or "yourself", as in "what's your name". It is
// a weaker sign than selfSubject: "your" can sit in a question about the
// user's own things.
var selfOwner = regexp.MustCompile(`(?i)\byour(self)?\b`)

// userThings matches "my" or "our", as in "does my repo send telemetry".
var userThings = regexp.MustCompile(`(?i)\b(my|our)\b`)

// userTask matches a request that asks Meru to do something for the user,
// such as "can you check my files for telemetry" or "could you send this
// to Sam". There "you" is the one doing the task, and the question is about
// the user's things, not about Meru.
var userTask = regexp.MustCompile(`(?i)^\s*(please\s+)?((can|could|would|will)\s+you\s+(please\s+)?)?` +
	`(search|find|look|check|read|show|list|open|summarize|summarise|draft|write|send|email|reply|` +
	`schedule|book|add|make|create|remind|tell me about my|explain my|compare|fix|review|translate|run|test)\b`)

// aboutMeru reports whether question asks about Meru itself. It must name
// something Meru is, does or sends (selfTopic), and not be a task Meru
// should do for the user (userTask). Then a question with Meru as its
// subject counts, as in "do you store my data in the cloud". One that only
// says "your" counts unless it also says "my" or "our": "what is your
// model" is about Meru, but "is your copy of my notes current" may not be.
func aboutMeru(question string) bool {
	q := strings.TrimSpace(question)
	if !selfTopic.MatchString(q) || userTask.MatchString(q) {
		return false
	}
	if selfSubject.MatchString(q) {
		return true
	}
	return selfOwner.MatchString(q) && !userThings.MatchString(q)
}

// selfIntroRule heads the facts about_meru gave before the first round, so
// the model answers from them and reads "you" as Meru.
const selfIntroRule = `The user asks about you, Meru. "You" means Meru, not the user's files or projects. ` +
	`Answer from these facts, which merud gave through about_meru; say so when they don't cover the question.`

// selfFirst runs about_meru before the model's first round, for a question
// about Meru itself, and returns its facts as a section of the prompt, or ""
// when about_meru is off or gave nothing. The call goes through dispatch,
// as runWebFirst's do, so it gets its tool_call and tool_result events, its
// transcript lines and its tool_calls row. It fails only when emit fails or
// ctx ends.
func (a *Agent) selfFirst(ctx context.Context, t *turn, all []engine.ToolSpec) (string, error) {
	if !offersTool(all, builtin.AboutMeru) {
		return "", nil
	}
	ran, err := a.runCalls(ctx, t, []engine.ToolCall{{Name: builtin.AboutMeru, Arguments: []byte("{}")}}, dispatch.CallerMeru)
	if err != nil {
		return "", err
	}
	if len(ran) == 0 || ran[0].outcome != dispatch.OutcomeOK || strings.TrimSpace(ran[0].msg.Content) == "" {
		a.log.DebugContext(ctx, "about_meru gave nothing before the first round; the model can call it itself")
		return "", nil
	}
	return selfIntroRule + "\n\nAbout Meru\n\n" + ran[0].msg.Content, nil
}
