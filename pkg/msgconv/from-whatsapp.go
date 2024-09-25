// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"context"
	"fmt"
	"html"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	_ "golang.org/x/image/webp"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

type contextKey int

const (
	contextKeyClient contextKey = iota
	contextKeyIntent
	contextKeyPortal
)

func getClient(ctx context.Context) *whatsmeow.Client {
	return ctx.Value(contextKeyClient).(*whatsmeow.Client)
}

func getIntent(ctx context.Context) bridgev2.MatrixAPI {
	return ctx.Value(contextKeyIntent).(bridgev2.MatrixAPI)
}

func getPortal(ctx context.Context) *bridgev2.Portal {
	return ctx.Value(contextKeyPortal).(*bridgev2.Portal)
}

func (mc *MessageConverter) getBasicUserInfo(ctx context.Context, user networkid.UserID) (id.UserID, string, error) {
	ghost, err := mc.Bridge.GetGhostByID(ctx, user)
	if err != nil {
		return "", "", fmt.Errorf("failed to get ghost by ID: %w", err)
	}
	login := mc.Bridge.GetCachedUserLoginByID(networkid.UserLoginID(user))
	if login != nil {
		return login.UserMXID, ghost.Name, nil
	}
	return ghost.Intent.GetMXID(), ghost.Name, nil
}

func (mc *MessageConverter) addMentions(ctx context.Context, mentionedJID []string, into *event.MessageEventContent) {
	if len(mentionedJID) == 0 {
		return
	}
	into.EnsureHasHTML()
	for _, jid := range mentionedJID {
		parsed, err := types.ParseJID(jid)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to parse mentioned JID")
			continue
		}
		mxid, displayname, err := mc.getBasicUserInfo(ctx, waid.MakeUserID(parsed))
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Str("jid", jid).Msg("Failed to get user info")
			continue
		}
		into.Mentions.UserIDs = append(into.Mentions.UserIDs, mxid)
		mentionText := "@" + parsed.User
		into.Body = strings.ReplaceAll(into.Body, mentionText, displayname)
		into.FormattedBody = strings.ReplaceAll(into.FormattedBody, mentionText, fmt.Sprintf(`<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), html.EscapeString(displayname)))
	}
}

func (mc *MessageConverter) ToMatrix(
	ctx context.Context,
	portal *bridgev2.Portal,
	client *whatsmeow.Client,
	intent bridgev2.MatrixAPI,
	waMsg *waE2E.Message,
	info *types.MessageInfo,
) *bridgev2.ConvertedMessage {
	ctx = context.WithValue(ctx, contextKeyClient, client)
	ctx = context.WithValue(ctx, contextKeyIntent, intent)
	ctx = context.WithValue(ctx, contextKeyPortal, portal)
	var part *bridgev2.ConvertedMessagePart
	var contextInfo *waE2E.ContextInfo
	switch {
	case waMsg.Conversation != nil, waMsg.ExtendedTextMessage != nil:
		part, contextInfo = mc.convertTextMessage(ctx, waMsg)
	case waMsg.TemplateMessage != nil:
		part, contextInfo = mc.convertTemplateMessage(ctx, info, waMsg.TemplateMessage)
	case waMsg.HighlyStructuredMessage != nil:
		part, contextInfo = mc.convertTemplateMessage(ctx, info, waMsg.HighlyStructuredMessage.GetHydratedHsm())
	case waMsg.TemplateButtonReplyMessage != nil:
		part, contextInfo = mc.convertTemplateButtonReplyMessage(ctx, waMsg.TemplateButtonReplyMessage)
	case waMsg.ListMessage != nil:
		part, contextInfo = mc.convertListMessage(ctx, waMsg.ListMessage)
	case waMsg.ListResponseMessage != nil:
		part, contextInfo = mc.convertListResponseMessage(ctx, waMsg.ListResponseMessage)
	case waMsg.PollCreationMessage != nil:
		part, contextInfo = mc.convertPollCreationMessage(ctx, waMsg.PollCreationMessage)
	case waMsg.PollCreationMessageV2 != nil:
		part, contextInfo = mc.convertPollCreationMessage(ctx, waMsg.PollCreationMessageV2)
	case waMsg.PollCreationMessageV3 != nil:
		part, contextInfo = mc.convertPollCreationMessage(ctx, waMsg.PollCreationMessageV3)
	case waMsg.PollUpdateMessage != nil:
		part, contextInfo = mc.convertPollUpdateMessage(ctx, info, waMsg.PollUpdateMessage)
	case waMsg.EventMessage != nil:
		part, contextInfo = mc.convertEventMessage(ctx, waMsg.EventMessage)
	case waMsg.ImageMessage != nil:
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.ImageMessage, "photo")
	case waMsg.StickerMessage != nil:
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.StickerMessage, "sticker")
	case waMsg.VideoMessage != nil:
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.VideoMessage, "video attachment")
	case waMsg.PtvMessage != nil:
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.PtvMessage, "video message")
	case waMsg.AudioMessage != nil:
		typeName := "audio attachment"
		if waMsg.AudioMessage.GetPTT() {
			typeName = "voice message"
		}
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.AudioMessage, typeName)
	case waMsg.DocumentMessage != nil:
		part, contextInfo = mc.convertMediaMessage(ctx, waMsg.DocumentMessage, "file attachment")
	case waMsg.LocationMessage != nil:
		part, contextInfo = mc.convertLocationMessage(ctx, waMsg.LocationMessage)
	case waMsg.LiveLocationMessage != nil:
		part, contextInfo = mc.convertLiveLocationMessage(ctx, waMsg.LiveLocationMessage)
	case waMsg.ContactMessage != nil:
		part, contextInfo = mc.convertContactMessage(ctx, waMsg.ContactMessage)
	case waMsg.ContactsArrayMessage != nil:
		part, contextInfo = mc.convertContactsArrayMessage(ctx, waMsg.ContactsArrayMessage)
	case waMsg.GroupInviteMessage != nil:
		part, contextInfo = mc.convertGroupInviteMessage(ctx, info, waMsg.GroupInviteMessage)
	case waMsg.ProtocolMessage != nil && waMsg.ProtocolMessage.GetType() == waE2E.ProtocolMessage_EPHEMERAL_SETTING:
		part, contextInfo = mc.convertEphemeralSettingMessage(ctx, waMsg.ProtocolMessage)
	default:
		part, contextInfo = mc.convertUnknownMessage(ctx, waMsg)
	}

	part.Content.Mentions = &event.Mentions{}
	if part.DBMetadata == nil {
		part.DBMetadata = &waid.MessageMetadata{}
	}
	dbMeta := part.DBMetadata.(*waid.MessageMetadata)
	dbMeta.SenderDeviceID = info.Sender.Device
	if info.IsIncomingBroadcast() {
		dbMeta.BroadcastListJID = &info.Chat
		if part.Extra == nil {
			part.Extra = map[string]any{}
		}
		part.Extra["fi.mau.whatsapp.source_broadcast_list"] = info.Chat.String()
	}
	mc.addMentions(ctx, contextInfo.GetMentionedJID(), part.Content)

	cm := &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{part},
	}
	if contextInfo.GetExpiration() > 0 {
		cm.Disappear.Timer = time.Duration(contextInfo.GetExpiration()) * time.Second
		cm.Disappear.Type = database.DisappearingTypeAfterRead
		if portal.Disappear.Timer != cm.Disappear.Timer && portal.Metadata.(*waid.PortalMetadata).DisappearingTimerSetAt < contextInfo.GetEphemeralSettingTimestamp() {
			portal.UpdateDisappearingSetting(ctx, cm.Disappear, intent, info.Timestamp, true, true)
		}
	}
	if contextInfo.GetStanzaID() != "" {
		pcp, _ := types.ParseJID(contextInfo.GetParticipant())
		chat, _ := types.ParseJID(contextInfo.GetRemoteJID())
		if chat.IsEmpty() {
			chat, _ = waid.ParsePortalID(portal.ID)
		}
		cm.ReplyTo = &networkid.MessageOptionalPartID{
			MessageID: waid.MakeMessageID(chat, pcp, contextInfo.GetStanzaID()),
		}
	}

	return cm
}
