package messages

import (
	"context"
	"fmt"

	"github.com/thirdeye/overseer/internal/entity"
)

// GetLatestMessage fetches the message a change notification points at
// (spec: usecase/messages/GetLatestMessage).
type GetLatestMessage struct {
	Graph MessageGetter
}

func (uc *GetLatestMessage) Execute(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	msg, err := uc.Graph.GetMessage(ctx, accessToken, messageID)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("get message %s: %w", messageID, err)
	}
	return msg, nil
}
