package main

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"unicode/utf8"

	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"

	"github.com/dipdup-net/go-lib/tzkt/api"
	"github.com/dipdup-net/metadata/cmd/metadata/models"
	"github.com/dipdup-net/metadata/cmd/metadata/resolver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

// TokenInfo -
type TokenInfo struct {
	TokenID   uint64            `json:"token_id"`
	TokenInfo map[string]string `json:"token_info"`
	Link      string            `json:"-"`
}

type tokenMetadataBigMap struct {
	TokenID   string            `json:"token_id"`
	TokenInfo map[string]string `json:"token_info"`
}

// UnmarshalJSON -
func (tokenInfo *TokenInfo) UnmarshalJSON(data []byte) error {
	var ti tokenMetadataBigMap
	if err := json.Unmarshal(data, &ti); err != nil {
		return err
	}

	tokenID, err := strconv.ParseUint(ti.TokenID, 16, 64)
	if err != nil {
		return err
	}
	tokenInfo.TokenID = tokenID
	tokenInfo.TokenInfo = ti.TokenInfo

	if link, ok := tokenInfo.TokenInfo[""]; ok {
		b, err := hex.DecodeString(link)
		if err != nil {
			return err
		}
		tokenInfo.Link = string(b)
		delete(tokenInfo.TokenInfo, "")
	}

	decodeMap(tokenInfo.TokenInfo)

	return nil
}

func decodeMap(m map[string]string) {
	for key, value := range m {
		if b, err := hex.DecodeString(value); err == nil {
			if utf8.Valid(b) {
				m[key] = string(b)
			}
		}
	}
}

func (indexer *Indexer) processTokenMetadata(update api.BigMapUpdate, tx *gorm.DB) error {
	var tokenInfo TokenInfo
	if err := json.Unmarshal(update.Content.Value, &tokenInfo); err != nil {
		return err
	}

	metadata, err := json.Marshal(tokenInfo.TokenInfo)
	if err != nil {
		return err
	}

	token := models.TokenMetadata{
		Network:  indexer.network,
		Contract: update.Contract.Address,
		TokenID:  tokenInfo.TokenID,
		Link:     tokenInfo.Link,
		Status:   models.StatusNew,
		Metadata: metadata,
	}

	if _, err := url.ParseRequestURI(token.Link); err != nil {
		token.Status = models.StatusApplied
	}

	return tx.Save(&token).Error
}

func (indexer *Indexer) logTokenMetadata(tm models.TokenMetadata, str, level string) {
	entry := indexer.log().WithField("contract", tm.Contract).WithField("token_id", tm.TokenID).WithField("link", tm.Link)
	switch level {
	case "info":
		entry.Info(str)
	case "warn":
		entry.Warn(str)
	case "error":
		entry.Error(str)
	}
}

func (indexer *Indexer) resolveTokenMetadata(tm *models.TokenMetadata) error {
	indexer.logTokenMetadata(*tm, "Trying to resolve", "info")
	data, err := indexer.resolver.Resolve(tm.Network, tm.Contract, tm.Link)
	if err != nil {
		switch {
		case errors.Is(err, resolver.ErrNoIPFSResponse) || errors.Is(err, resolver.ErrTezosStorageKeyNotFound):
			tm.RetryCount += 1
			if tm.RetryCount < int(indexer.maxRetryCount) {
				indexer.logTokenMetadata(*tm, fmt.Sprintf("Retry: %s", err.Error()), "warn")
			} else {
				tm.Status = models.StatusFailed
				indexer.logTokenMetadata(*tm, "Failed", "warn")
			}
		default:
			tm.Status = models.StatusFailed
			indexer.logTokenMetadata(*tm, "Failed", "warn")
		}
	} else {
		metadata, err := mergeTokenMetadata(tm.Metadata, data)
		if err != nil {
			return err
		}
		tm.Metadata = metadata
		tm.Status = models.StatusApplied
	}
	return nil
}

func mergeTokenMetadata(src, got []byte) ([]byte, error) {
	if len(src) == 0 {
		return got, nil
	}

	if len(got) == 0 {
		return src, nil
	}

	srcMap := make(map[string]interface{})
	if err := json.Unmarshal(src, &srcMap); err != nil {
		return nil, err
	}
	gotMap := make(map[string]interface{})
	if err := json.Unmarshal(got, &gotMap); err != nil {
		return nil, err
	}

	for key, value := range gotMap {
		if _, ok := srcMap[key]; !ok {
			srcMap[key] = value
		}
	}

	return json.Marshal(srcMap)
}

func (indexer *Indexer) onTokenFlush(tx *gorm.DB, flushed []interface{}) error {
	if len(flushed) == 0 {
		return nil
	}

	return indexer.db.Transaction(func(tx *gorm.DB) error {
		for i := range flushed {
			tm, ok := flushed[i].(*models.TokenMetadata)
			if !ok {
				return errors.Errorf("Invalid token's queue type: %T", flushed[i])
			}
			if err := tx.Clauses(clause.OnConflict{
				UpdateAll: true,
			}).Create(tm).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (indexer *Indexer) onTokenTick(tx *gorm.DB) error {
	uresolved, err := models.GetTokenMetadata(indexer.db, models.StatusNew, 15, 0)
	if err != nil {
		return err
	}
	for i := range uresolved {
		if uresolved[i].Status != models.StatusApplied {
			if err := indexer.resolveTokenMetadata(&uresolved[i]); err != nil {
				return err
			}
		}
		indexer.tokens.Add(&uresolved[i])
	}
	return nil
}