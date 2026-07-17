// AskUserQuestion: an interactive approval carried as a pending_approvals
// item with a `questions` array. The client renders a form and answers via
// approval_response{decision:"allow", updated_input}. The updated_input
// preserves the original tool input and adds an `answers` map keyed by the
// question prompt — mirroring the verified native contract
// (letta-code 0.28.x: ask-user-question.ts + InlineQuestionApproval.tsx).
//
// NOTE (protocol.ts ambiguity): protocol.ts types updated_input only as
// `Record<string, unknown> | null` and PendingApproval.questions as
// {id,prompt,options?,multi?}. The exact answer key the server expects is not
// pinned in the schema; we key answers by prompt (the native behavior) and
// carry the original input through. AskUserQuestion is a Stage-2 surface, so
// this is best-effort for the slice.
package main

import (
	"strings"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
)

const otherSentinel = "\x00other"

type questionForm struct {
	id        string // approval id (tool_call_id)
	approval  protocol.PendingApproval
	questions []protocol.ApprovalQuestion
	form      *form.Form
	single    []string   // bound values for single-select questions
	multi     [][]string // bound values for multi-select questions
	other     []string   // bound values for per-question "Other" inputs
}

// newQuestionForm builds an interactive form from an approval's questions, or
// nil when the approval carries none.
func newQuestionForm(a protocol.PendingApproval) *questionForm {
	if len(a.Questions) == 0 {
		return nil
	}
	qf := &questionForm{
		id:        a.ID,
		approval:  a,
		questions: a.Questions,
		single:    make([]string, len(a.Questions)),
		multi:     make([][]string, len(a.Questions)),
		other:     make([]string, len(a.Questions)),
	}

	var fields []form.Field
	for i, q := range a.Questions {
		i := i
		var opts []form.Option
		for _, o := range q.Options {
			opts = append(opts, form.Option{Label: o, Value: o})
		}
		opts = append(opts, form.Option{Label: "Other (type an answer)", Value: otherSentinel})
		if q.Multi {
			fields = append(fields, form.NewMultiSelect(q.Prompt).Options(opts...).Value(&qf.multi[i]))
		} else {
			fields = append(fields, form.NewSelect(q.Prompt).Options(opts...).Value(&qf.single[i]))
		}
		fields = append(fields, form.NewInput("Your answer").Value(&qf.other[i]).
			WithHide(func() bool {
				if qf.questions[i].Multi {
					for _, v := range qf.multi[i] {
						if v == otherSentinel {
							return false
						}
					}
					return true
				}
				return qf.single[i] != otherSentinel
			}))
	}

	qf.form = form.New(fields...)
	return qf
}

// answers builds the answer map keyed by question prompt.
func (qf *questionForm) answers() map[string]string {
	out := map[string]string{}
	for i, q := range qf.questions {
		if q.Multi {
			var vals []string
			for _, v := range qf.multi[i] {
				if v == otherSentinel {
					if strings.TrimSpace(qf.other[i]) != "" {
						vals = append(vals, strings.TrimSpace(qf.other[i]))
					}
					continue
				}
				vals = append(vals, v)
			}
			out[q.Prompt] = strings.Join(vals, ", ")
			continue
		}
		v := qf.single[i]
		if v == otherSentinel {
			v = strings.TrimSpace(qf.other[i])
		}
		out[q.Prompt] = v
	}
	return out
}

// response builds the approval_response event: allow, with updated_input
// preserving the original tool input plus an `answers` map.
func (qf *questionForm) response() protocol.ApprovalResponse {
	updated := map[string]any{}
	for k, v := range qf.approval.Input {
		updated[k] = v
	}
	answers := map[string]any{}
	for k, v := range qf.answers() {
		answers[k] = v
	}
	updated["answers"] = answers

	resp := protocol.NewApprovalResponse(qf.id, "allow")
	resp.UpdatedInput = updated
	return resp
}
