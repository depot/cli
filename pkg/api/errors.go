package api

import "encoding/json"

type ErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func tryParseErrorResponse(body []byte) (*ErrorResponse, error) {
	var response ErrorResponse
	err := json.Unmarshal(body, &response)
	if err != nil {
		return nil, err
	}
	if response.OK {
		return nil, nil
	}
	return &response, nil
}
