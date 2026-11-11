package transfer

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/kingfs/Trajecta/internal/store"
)

// Channel identity on the wire of a transfer is (normalised base_url,
// plaintext api key). Two source deployments that were ever configured with
// the same endpoint and the same credential are the same channel, no matter
// what operators named them; the same endpoint with a different credential is
// a different channel.

func normalizeBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return strings.ToLower(strings.TrimRight(s, "/"))
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	// Fragments do not route anywhere; drop them. Query strings are kept
	// verbatim (they are rare and meaningful to some gateways).
	u.Fragment = ""
	return u.String()
}

func channelMatchKey(baseURL, apiKey string) string {
	return normalizeBaseURL(baseURL) + "\x00" + apiKey
}

// targetChannel is a minimal projection of one target deployment's channel.
type targetChannel struct {
	id             string
	name           string
	baseURL        string
	providerPreset string
	enabled        bool
	apiKey         string // decrypted; "" when absent or undecryptable
	keyDecryptErr  error
}

func loadTargetChannels(ctx context.Context, view *store.Store) ([]targetChannel, error) {
	rows, err := view.TransferQuery(ctx, `SELECT id, name, base_url, provider_preset, enabled, api_key_ciphertext FROM channel_configs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []targetChannel
	for rows.Next() {
		var tc targetChannel
		var keyBlob []byte
		var enabled any
		if err := rows.Scan(&tc.id, &tc.name, &tc.baseURL, &tc.providerPreset, &enabled, &keyBlob); err != nil {
			return nil, err
		}
		tc.enabled = anyBool(enabled)
		if len(keyBlob) > 0 {
			key, err := view.TransferDecryptSecretString(string(keyBlob))
			if err != nil {
				tc.keyDecryptErr = err
			} else {
				tc.apiKey = key
			}
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// channelResolver decides, for every source channel, whether it merges into
// a target channel or is created as a new one. It owns the source→target id
// map every other table consults for channel references.
type channelResolver struct {
	idMap   map[string]string
	log     *Logger
	cmap    map[string]string
	created []string
	reused  []ChannelReuse
	// channelsTouched marks channels whose rows (existing or new) received at
	// least one reference during this import; used for the initialized marker.
	channelsTouched map[string]bool
	// strategyCreate forces every un-mapped source channel to become a new
	// channel instead of merging into an id/endpoint match.
	strategyCreate bool
}

type ChannelReuse struct {
	SourceID   string `json:"source_id"`
	TargetID   string `json:"target_id"`
	Matched    string `json:"matched"` // "map" | "id" | "base_url+key" | "create"
	BaseURL    string `json:"base_url"`
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
	TargetKept bool   `json:"target_kept"` // target row kept verbatim (always true for merges)
}

func newChannelResolver(log *Logger, channelMap map[string]string) *channelResolver {
	return &channelResolver{
		idMap:           map[string]string{},
		cmap:            channelMap,
		log:             log,
		channelsTouched: map[string]bool{},
	}
}

// resolve maps one source channel onto a target channel id. It returns the
// target id and whether the target row already existed (merge) or was created
// by this import.
func (r *channelResolver) resolve(src channelConfigRow, target []targetChannel, targetByID map[string]*targetChannel, targetByKey map[string]*targetChannel) (dstID string, existed bool, err error) {
	// 1. Explicit operator mapping wins.
	if dst, ok := r.cmap[src.ID]; ok {
		t, ok := targetByID[dst]
		if !ok {
			return "", false, fmt.Errorf("--channel-map %q -> %q: target channel %q does not exist", src.ID, dst, dst)
		}
		r.idMap[src.ID] = dst
		r.reused = append(r.reused, ChannelReuse{SourceID: src.ID, TargetID: dst, Matched: "map", BaseURL: src.BaseURL, SourceName: src.Name, TargetName: t.name, TargetKept: true})
		r.channelsTouched[dst] = true
		r.log.Infof("channel %q (%s) mapped onto existing channel %q (%s) by --channel-map", src.Name, src.ID, t.name, dst)
		return dst, true, nil
	}
	if r.strategyCreate {
		// --channel-strategy create: never merge, always create unless the
		// operator mapped the channel explicitly (handled above).
		newID := src.ID
		if _, collision := targetByID[newID]; collision {
			newID = uuid.NewString()
		}
		r.idMap[src.ID] = newID
		r.created = append(r.created, newID)
		r.channelsTouched[newID] = true
		r.log.Infof("channel %q (%s) creates new channel %s (--channel-strategy create)", src.Name, src.ID, newID)
		return newID, false, nil
	}
	// 2. Exact id reuse: the source channel id already exists in the target
	//    and has the same endpoint; this is the common "same deployment
	//    recorded twice" case (e.g. re-importing after an archive).
	if t, ok := targetByID[src.ID]; ok {
		if normalizeBaseURL(t.baseURL) == normalizeBaseURL(src.BaseURL) && t.apiKey == src.APIKey {
			r.idMap[src.ID] = src.ID
			r.reused = append(r.reused, ChannelReuse{SourceID: src.ID, TargetID: src.ID, Matched: "id", BaseURL: src.BaseURL, SourceName: src.Name, TargetName: t.name, TargetKept: true})
			r.channelsTouched[src.ID] = true
			r.log.Infof("channel %q (%s) matches target channel with the same id; keeping target row", src.Name, src.ID)
			return src.ID, true, nil
		}
		// Id exists but provenance differs: reassign to avoid mixing two
		// different upstreams under one id.
		newID := uuid.NewString()
		r.idMap[src.ID] = newID
		r.created = append(r.created, newID)
		r.channelsTouched[newID] = true
		r.log.Warnf("channel %q (%s) id collides with a different target channel (%s); importing as new id %s", src.Name, src.ID, t.name, newID)
		return newID, false, nil
	}
	// 3. Natural identity: normalised base_url + plaintext api key.
	if t, ok := targetByKey[channelMatchKey(src.BaseURL, src.APIKey)]; ok {
		r.idMap[src.ID] = t.id
		r.reused = append(r.reused, ChannelReuse{SourceID: src.ID, TargetID: t.id, Matched: "base_url+key", BaseURL: src.BaseURL, SourceName: src.Name, TargetName: t.name, TargetKept: true})
		r.channelsTouched[t.id] = true
		r.log.Infof("channel %q (%s) matches existing channel %q (%s) by base_url+api key; skipping channel row, unioning models", src.Name, src.ID, t.name, t.id)
		return t.id, true, nil
	}
	// 4. New channel.
	newID := src.ID
	if _, collision := targetByID[newID]; collision {
		newID = uuid.NewString()
	}
	r.idMap[src.ID] = newID
	r.created = append(r.created, newID)
	r.channelsTouched[newID] = true
	r.log.Infof("channel %q (%s) creates new channel %s", src.Name, src.ID, newID)
	return newID, false, nil
}

func (r *channelResolver) remapID(sourceID string) string {
	if mapped, ok := r.idMap[sourceID]; ok {
		return mapped
	}
	return sourceID
}

func (r *channelResolver) remapRouteTarget(v string) string {
	if v == "" {
		return v
	}
	id, cred, found := strings.Cut(v, ":")
	if mapped, ok := r.idMap[id]; ok {
		if found {
			return mapped + ":" + cred
		}
		return mapped
	}
	return v
}

func buildTargetMaps(target []targetChannel) (byID map[string]*targetChannel, byKey map[string]*targetChannel) {
	byID = make(map[string]*targetChannel, len(target))
	byKey = make(map[string]*targetChannel, len(target))
	for i := range target {
		t := &target[i]
		byID[t.id] = t
		if t.keyDecryptErr != nil {
			// Undecryptable key cannot match; skip it for key matching only.
			continue
		}
		key := channelMatchKey(t.baseURL, t.apiKey)
		if _, exists := byKey[key]; !exists {
			byKey[key] = t
		}
	}
	return byID, byKey
}
