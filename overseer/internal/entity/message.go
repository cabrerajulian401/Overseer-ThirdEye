package entity

import "time"

// EmailMessage is the Graph mail payload passed between layers. Mail is never
// stored — threads are re-fetched from Graph when needed.
type EmailMessage struct {
	ID                string // Graph message id
	ConversationID    string
	ConversationIndex string
	From              string // sender address
	FromName          string // sender display name
	Subject           string
	Body              string
	BodyPreview       string
	WebLink           string
	CreatedDateTime   time.Time
}

// Thread is a conversation's messages, ordered oldest → newest.
type Thread struct {
	ConversationID string
	Messages       []EmailMessage
}

// LatestMessage returns the newest message in the thread, or a zero value for
// an empty thread.
func (t Thread) LatestMessage() EmailMessage {
	if len(t.Messages) == 0 {
		return EmailMessage{}
	}
	return t.Messages[len(t.Messages)-1]
}
