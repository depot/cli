package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

type InitResponse struct {
	BuildId string `json:"id"`
	BuildIp string `json:"ip"`
}

const ApiUri = "https://api.depot.dev/builds"
const SimpleAuthToken = "4a4bd8307b37497b906d7b92574ccac4"

func InitBuild() (*InitResponse, error) {
	client := &http.Client{}
	payload := fmt.Sprintf(`{"key": "%s"}`, "")
	jsonStr := []byte(payload)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", ApiUri, "init"), bytes.NewBuffer(jsonStr))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", SimpleAuthToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response InitResponse
	json.Unmarshal([]byte(body), &response)
	return &response, nil
}
