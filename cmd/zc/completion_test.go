package main

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
)

func TestPathCompletion(t *testing.T) {
	ta := textarea.New()
	ta.SetValue("@cm")
	m := &model{input: ta, serverCWD: "/var/home/jmyers/projects/ZeptoCodeCLI"}
	m.refreshCompletions()
	if !m.completion.visible {
		t.Fatalf("expected visible completions, got %+v", m.completion)
	}
	found := false
	for _, it := range m.completion.items {
		t.Logf("item: %q", it.insert)
		if it.insert == "@cmd/" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected @cmd/ completion")
	}
}

func TestPathCompletionLiveish(t *testing.T) {
	ta := textarea.New()
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.SetWidth(100)
	ta.Focus()
	ta.SetValue("@proj")
	m := &model{input: ta, serverCWD: "/var/home/jmyers"}
	t.Logf("value=%q", m.input.Value())
	m.refreshCompletions()
	t.Logf("state=%+v", m.completion)
	if !m.completion.visible {
		t.Fatal("expected visible")
	}
}
