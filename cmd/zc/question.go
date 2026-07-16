// AskUserQuestion: an interactive approval, not an allow/deny decision.
// The client renders the question form and answers via approval_response
// allow with updated_input REPLACING the tool args entirely:
//
//	{ "questions": [...original...], "answers": { "<question text>": "<answer>" } }
//
// Answer values are plain strings; multiSelect answers join the selected
// labels with ", " (verified against letta-code 0.28.8:
// src/tools/impl/ask-user-question.ts + InlineQuestionApproval.tsx).
package main

import (
	"encoding/json"
	"strings"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
)

const otherSentinel = "\x00other"

type questionSpec struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

type questionForm struct {
	req       *protocol.ControlRequest
	questions []questionSpec
	form      *form.Form
	inited    bool       // forms must Init() through the tea loop
	single    []string   // bound values for single-select questions
	multi     [][]string // bound values for multi-select questions
	other     []string   // bound values for per-question "Other" inputs
}

// isQuestionRequest reports whether a control request is AskUserQuestion.
func isQuestionRequest(req *protocol.ControlRequest) bool {
	return req.Request.ToolName == "AskUserQuestion"
}

func newQuestionForm(req *protocol.ControlRequest) *questionForm {
	raw, err := json.Marshal(req.Request.Input)
	if err != nil {
		return nil
	}
	var input struct {
		Questions []questionSpec `json:"questions"`
	}
	if json.Unmarshal(raw, &input) != nil || len(input.Questions) == 0 {
		return nil
	}

	qf := &questionForm{
		req:       req,
		questions: input.Questions,
		single:    make([]string, len(input.Questions)),
		multi:     make([][]string, len(input.Questions)),
		other:     make([]string, len(input.Questions)),
	}

	var fields []form.Field
	for i, q := range input.Questions {
		i := i
		title := q.Question
		if q.Header != "" {
			title = q.Header + ": " + q.Question
		}
		var opts []form.Option
		for _, o := range q.Options {
			label := o.Label
			if o.Description != "" {
				label += "  — " + compactOneLine(o.Description, 60)
			}
			opts = append(opts, form.Option{Label: label, Value: o.Label})
		}
		opts = append(opts, form.Option{Label: "Other (type an answer)", Value: otherSentinel})
		if q.MultiSelect {
			fields = append(fields,
				form.NewMultiSelect(title).Options(opts...).Value(&qf.multi[i]))
		} else {
			fields = append(fields,
				form.NewSelect(title).Options(opts...).Value(&qf.single[i]))
		}
		// Free-text follow-up, shown only when "Other" was chosen.
		fields = append(fields, form.NewInput("Your answer").Value(&qf.other[i]).
			WithHide(func() bool {
				if input.Questions[i].MultiSelect {
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

// answers builds the answers map per the wire contract.
func (qf *questionForm) answers() map[string]string {
	out := map[string]string{}
	for i, q := range qf.questions {
		if q.MultiSelect {
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
			out[q.Question] = strings.Join(vals, ", ")
			continue
		}
		v := qf.single[i]
		if v == otherSentinel {
			v = strings.TrimSpace(qf.other[i])
		}
		out[q.Question] = v
	}
	return out
}

// decision builds the approval decision: allow with updated_input replacing
// the tool args (questions must be carried through — the tool validates them).
func (qf *questionForm) decision() protocol.ApprovalDecision {
	questionsRaw := qf.req.Request.Input["questions"]
	return protocol.ApprovalDecision{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"questions": questionsRaw,
			"answers":   qf.answers(),
		},
	}
}
