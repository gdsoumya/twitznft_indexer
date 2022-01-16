package util

import (
	"encoding/json"
	"strconv"
	"strings"
)

type Attribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Attributes struct {
	Name       string      `json:"name"`
	Attributes []Attribute `json:"attributes"`
}

func ParseTweetFromMetadata(metadata []byte) (string, string, error) {
	attr := Attributes{}
	if err := json.Unmarshal(metadata, &attr); err != nil {
		return "", "", err
	}
	tweetID, creatorID := "", ""
	if attr.Name != "" {
		splits := strings.Split(attr.Name, " ")
		if len(splits) > 1 && strings.HasPrefix(splits[1], "#") {
			id := strings.TrimPrefix(splits[1], "#")
			if _, err := strconv.Atoi(id); err == nil {
				tweetID = id
			}
		}
	}

	for _, at := range attr.Attributes {
		if at.Name == "twitter_creator_id" {
			creatorID = at.Value
		}
	}
	return tweetID, creatorID, nil
}
