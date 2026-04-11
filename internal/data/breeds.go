package data

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed breeds.json
var breedsJSON []byte

type breedsData struct {
	CatBreeds []string `json:"catBreeds"`
	DogBreeds []string `json:"dogBreeds"`
}

var (
	catBreeds map[string]bool
	dogBreeds map[string]bool
)

func init() {
	var data breedsData
	if err := json.Unmarshal(breedsJSON, &data); err != nil {
		panic("failed to parse breeds.json: " + err.Error())
	}

	catBreeds = make(map[string]bool, len(data.CatBreeds))
	for _, b := range data.CatBreeds {
		catBreeds[strings.ToLower(b)] = true
	}

	dogBreeds = make(map[string]bool, len(data.DogBreeds))
	for _, b := range data.DogBreeds {
		dogBreeds[strings.ToLower(b)] = true
	}
}

// IsValidBreed checks if the breed name is valid for the given species.
// For dog/cat it validates against the known list; for other species it accepts anything.
func IsValidBreed(species, breed string) bool {
	if breed == "" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(breed))
	s := strings.ToLower(strings.TrimSpace(species))

	switch {
	case s == "dog" || s == "ძაღლი":
		return dogBreeds[lower]
	case s == "cat" || s == "კატა":
		return catBreeds[lower]
	default:
		return true // other species: accept any breed
	}
}
