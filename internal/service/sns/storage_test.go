package sns

import (
	"errors"
	"testing"
)

func TestCreateTopicRejectsDelimiterNames(t *testing.T) {
	t.Parallel()

	tests := []string{
		"bad:name",
		"bad/name",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := NewMemoryStorage("http://localhost:4566")

			_, err := store.CreateTopic(t.Context(), name, nil, nil)
			expectTopicErrorCode(t, err)
		})
	}
}

func TestCreateTopicAcceptsValidNames(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage("http://localhost:4566")

	for _, name := range []string{"topic_name-1"} {
		topic, err := store.CreateTopic(t.Context(), name, nil, nil)
		if err != nil {
			t.Fatalf("CreateTopic(%q): %v", name, err)
		}

		if topic.Name != name {
			t.Fatalf("CreateTopic(%q) name = %q", name, topic.Name)
		}
	}

	// A .fifo name is only valid together with the FifoTopic attribute.
	topic, err := store.CreateTopic(t.Context(), "topic-name.fifo", map[string]string{topicAttrFifoTopic: testAttrValueTrue}, nil)
	if err != nil {
		t.Fatalf("CreateTopic(fifo): %v", err)
	}

	if topic.Name != "topic-name.fifo" || !topic.Fifo {
		t.Fatalf("CreateTopic(fifo) = %+v", topic)
	}
}

func expectTopicErrorCode(t *testing.T, err error) {
	t.Helper()

	code := errInvalidParameter

	var topicErr *TopicError
	if !errors.As(err, &topicErr) {
		t.Fatalf("got err %v, want TopicError code %s", err, code)
	}

	if topicErr.Code != code {
		t.Fatalf("got TopicError code %s, want %s", topicErr.Code, code)
	}
}
