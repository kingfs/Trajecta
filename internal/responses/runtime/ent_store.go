package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/kingfs/Trajecta/ent/dao"
	entresponse "github.com/kingfs/Trajecta/ent/dao/response"
	"github.com/kingfs/Trajecta/ent/dao/responseitem"
	"github.com/kingfs/Trajecta/internal/responses/protocol"
)

// responseItemInsertChunk bounds how many response items one multi-row insert
// carries, the same way the store bounds its own bulk statements.
const responseItemInsertChunk = 200

type EntStore struct {
	client *dao.Client
}

func NewEntStore(client *dao.Client) *EntStore {
	return &EntStore{client: client}
}

func (s *EntStore) Put(ctx context.Context, resp protocol.Response, req protocol.CreateResponseRequest, inputItems []protocol.InputItem, outputItems []protocol.OutputItem) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	client := tx.Client()
	conversationID := codexConversationID(resp.Metadata)
	historyItemIDs := make([]string, 0, len(inputItems)+len(outputItems))
	outputItemIDs := make([]string, 0, len(outputItems))

	// Every item of the response goes in with multi-row inserts. The per-item
	// form issued one INSERT per input and output item of the conversation
	// history, which is the largest statement count on the Responses write path.
	builders := make([]*dao.ResponseItemCreate, 0, len(inputItems)+len(outputItems))
	for i, input := range inputItems {
		id := storedItemID(resp.ID, "input", input.ID, i)
		payload, err := jsonMap(input)
		if err != nil {
			return rollback(tx, err)
		}
		builders = append(builders, client.ResponseItem.Create().
			SetID(id).
			SetKind(responseitem.KindInput).
			SetResponseID(resp.ID).
			SetConversationID(conversationID).
			SetPayload(payload))
		historyItemIDs = append(historyItemIDs, id)
	}
	for i, output := range outputItems {
		id := storedItemID(resp.ID, "output", output.ID, i)
		payload, err := jsonMap(output)
		if err != nil {
			return rollback(tx, err)
		}
		builders = append(builders, client.ResponseItem.Create().
			SetID(id).
			SetKind(responseitem.KindOutput).
			SetResponseID(resp.ID).
			SetConversationID(conversationID).
			SetPayload(payload))
		historyItemIDs = append(historyItemIDs, id)
		outputItemIDs = append(outputItemIDs, id)
	}
	for start := 0; start < len(builders); start += responseItemInsertChunk {
		end := start + responseItemInsertChunk
		if end > len(builders) {
			end = len(builders)
		}
		if err := client.ResponseItem.CreateBulk(builders[start:end]...).Exec(ctx); err != nil {
			return rollback(tx, err)
		}
	}

	createdAt := time.Unix(resp.CreatedAt, 0)
	if resp.CreatedAt == 0 {
		createdAt = time.Now()
	}
	if err := client.Response.Create().
		SetID(resp.ID).
		SetConversationID(conversationID).
		SetPreviousResponseID(resp.PreviousResponseID).
		SetStatus(entresponse.Status(resp.Status)).
		SetModel(resp.Model).
		SetHistoryItemIds(historyItemIDs).
		SetOutputItemIds(outputItemIDs).
		SetEffectiveTools(toolsJSON(req.Tools)).
		SetMetadata(nilToEmptyMap(resp.Metadata)).
		SetUsage(usageJSON(resp.Usage)).
		SetError(map[string]any{}).
		SetCreatedAt(createdAt).
		Exec(ctx); err != nil {
		return rollback(tx, err)
	}
	return tx.Commit()
}

func (s *EntStore) Get(ctx context.Context, id string) (protocol.Response, bool, error) {
	row, err := s.client.Response.Get(ctx, id)
	if dao.IsNotFound(err) {
		return protocol.Response{}, false, nil
	}
	if err != nil {
		return protocol.Response{}, false, err
	}
	output, err := s.outputItems(ctx, row.OutputItemIds)
	if err != nil {
		return protocol.Response{}, false, err
	}
	return responseFromRow(row, output), true, nil
}

func (s *EntStore) UpdateStatus(ctx context.Context, id string, status string) (protocol.Response, bool, error) {
	return s.UpdateStatusMetadata(ctx, id, status, nil)
}

func (s *EntStore) UpdateStatusMetadata(ctx context.Context, id string, status string, metadata map[string]any) (protocol.Response, bool, error) {
	builder := s.client.Response.UpdateOneID(id).
		SetStatus(entresponse.Status(status))
	if metadata != nil {
		current, err := s.client.Response.Get(ctx, id)
		if dao.IsNotFound(err) {
			return protocol.Response{}, false, nil
		}
		if err != nil {
			return protocol.Response{}, false, err
		}
		builder.SetMetadata(mergeMetadata(current.Metadata, metadata))
	}
	row, err := builder.Save(ctx)
	if dao.IsNotFound(err) {
		return protocol.Response{}, false, nil
	}
	if err != nil {
		return protocol.Response{}, false, err
	}
	output, err := s.outputItems(ctx, row.OutputItemIds)
	if err != nil {
		return protocol.Response{}, false, err
	}
	return responseFromRow(row, output), true, nil
}

func (s *EntStore) InputItems(ctx context.Context, id string) ([]protocol.InputItem, bool, error) {
	row, err := s.client.Response.Get(ctx, id)
	if dao.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	stored, err := s.itemsByID(ctx, row.HistoryItemIds)
	if err != nil {
		return nil, false, err
	}
	items := make([]protocol.InputItem, 0, len(row.HistoryItemIds))
	for _, itemID := range row.HistoryItemIds {
		entry, ok := stored[itemID]
		if !ok {
			return nil, false, s.missingItemError(ctx, itemID)
		}
		if entry.Kind != responseitem.KindInput {
			continue
		}
		input, err := inputItemFromPayload(entry.Payload)
		if err != nil {
			return nil, false, err
		}
		items = append(items, input)
	}
	return items, true, nil
}

func (s *EntStore) ContinuationItems(ctx context.Context, id string) ([]LedgerItem, bool, error) {
	ids := make([]string, 0, 8)
	rowsByID := make(map[string]*dao.Response, 8)
	itemsByID := make(map[string]*dao.ResponseItem)
	for current := id; current != ""; {
		row, err := s.client.Response.Get(ctx, current)
		if dao.IsNotFound(err) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		ids = append(ids, current)
		rowsByID[current] = row
		// The whole history of this step in one query: the compact boundary
		// check below and the ledger assembly further down both read it.
		stored, err := s.itemsByID(ctx, row.HistoryItemIds)
		if err != nil {
			return nil, false, err
		}
		for itemID, entry := range stored {
			itemsByID[itemID] = entry
		}
		hasCompactRequest, err := compactRequestIn(row.HistoryItemIds, stored)
		if err != nil {
			return nil, false, err
		}
		if hasCompactRequest || responseIsCodexCompactCandidate(row.Metadata) {
			break
		}
		current = row.PreviousResponseID
	}

	items := make([]LedgerItem, 0, len(ids)*2)
	for i := len(ids) - 1; i >= 0; i-- {
		row := rowsByID[ids[i]]
		for _, itemID := range row.HistoryItemIds {
			stored, ok := itemsByID[itemID]
			if !ok {
				return nil, false, s.missingItemError(ctx, itemID)
			}
			switch stored.Kind {
			case responseitem.KindInput:
				input, err := inputItemFromPayload(stored.Payload)
				if err != nil {
					return nil, false, err
				}
				items = append(items, LedgerItem{Input: &input})
			case responseitem.KindOutput:
				output, err := outputItemFromPayload(stored.Payload)
				if err != nil {
					return nil, false, err
				}
				items = append(items, LedgerItem{Output: &output})
			}
		}
	}
	return items, true, nil
}

func (s *EntStore) LatestResponseIDByConversation(ctx context.Context, conversationID string) (string, bool, error) {
	row, err := s.client.Response.Query().
		Where(entresponse.ConversationID(conversationID)).
		Order(entresponse.ByCreatedAt(sql.OrderDesc()), entresponse.ByID(sql.OrderDesc())).
		First(ctx)
	if dao.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.ID, true, nil
}

func (s *EntStore) outputItems(ctx context.Context, itemIDs []string) ([]protocol.OutputItem, error) {
	stored, err := s.itemsByID(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	items := make([]protocol.OutputItem, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		entry, ok := stored[itemID]
		if !ok {
			return nil, s.missingItemError(ctx, itemID)
		}
		output, err := outputItemFromPayload(entry.Payload)
		if err != nil {
			return nil, err
		}
		items = append(items, output)
	}
	return items, nil
}

// itemsByID loads the stored items with the given ids in one query, keyed by id.
// Every caller walks its own id list, which is what keeps the stored order
// without reading one row per id.
func (s *EntStore) itemsByID(ctx context.Context, ids []string) (map[string]*dao.ResponseItem, error) {
	items := make(map[string]*dao.ResponseItem, len(ids))
	if len(ids) == 0 {
		return items, nil
	}
	rows, err := s.client.ResponseItem.Query().Where(responseitem.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		items[row.ID] = row
	}
	return items, nil
}

// missingItemError reports an item id a response still references but that no
// longer exists, with the error a direct read of that id returned.
func (s *EntStore) missingItemError(ctx context.Context, itemID string) error {
	if _, err := s.client.ResponseItem.Get(ctx, itemID); err != nil {
		return err
	}
	return fmt.Errorf("response item %q not found", itemID)
}

// compactRequestIn reports whether the given history holds a compact request
// item. The caller passes the items it already loaded for that history.
func compactRequestIn(historyItemIDs []string, items map[string]*dao.ResponseItem) (bool, error) {
	for _, itemID := range historyItemIDs {
		stored, ok := items[itemID]
		if !ok {
			return false, fmt.Errorf("response item %q is missing from its own history", itemID)
		}
		if stored.Kind != responseitem.KindInput {
			continue
		}
		input, err := inputItemFromPayload(stored.Payload)
		if err != nil {
			return false, err
		}
		if input.Type == "compact_request" {
			return true, nil
		}
	}
	return false, nil
}

func responseFromRow(row *dao.Response, output []protocol.OutputItem) protocol.Response {
	return protocol.Response{
		ID:                 row.ID,
		Object:             "response",
		CreatedAt:          row.CreatedAt.Unix(),
		Status:             string(row.Status),
		Model:              row.Model,
		Output:             output,
		PreviousResponseID: row.PreviousResponseID,
		Usage: protocol.Usage{
			InputTokens:  intFromMap(row.Usage, "input_tokens"),
			OutputTokens: intFromMap(row.Usage, "output_tokens"),
			TotalTokens:  intFromMap(row.Usage, "total_tokens"),
		},
		Metadata: row.Metadata,
	}
}

func jsonMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func inputItemFromPayload(payload map[string]any) (protocol.InputItem, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return protocol.InputItem{}, err
	}
	var out protocol.InputItem
	if err := json.Unmarshal(raw, &out); err != nil {
		return protocol.InputItem{}, err
	}
	return out, nil
}

func outputItemFromPayload(payload map[string]any) (protocol.OutputItem, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return protocol.OutputItem{}, err
	}
	var out protocol.OutputItem
	if err := json.Unmarshal(raw, &out); err != nil {
		return protocol.OutputItem{}, err
	}
	return out, nil
}

func storedItemID(responseID, kind, rawID string, index int) string {
	if rawID == "" {
		rawID = "generated"
	}
	return fmt.Sprintf("%s/%s/%06d/%s", responseID, kind, index, rawID)
}

func nilToEmptyMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func usageJSON(usage protocol.Usage) map[string]any {
	return map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.TotalTokens,
	}
}

func toolsJSON(tools []protocol.Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		payload, err := jsonMap(tool)
		if err != nil {
			continue
		}
		out = append(out, payload)
	}
	return out
}

func intFromMap(values map[string]any, key string) int {
	switch v := values[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func rollback(tx *dao.Tx, err error) error {
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		return fmt.Errorf("%w: rollback failed: %v", err, rollbackErr)
	}
	return err
}
