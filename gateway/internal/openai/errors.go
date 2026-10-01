package openai

import (
	"encoding/json"
	"net/http"
)

// OpenAI 协议规定的错误信息协议.
type errorBody struct {
	Code    string  `json:"code"`
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Param   *string `json:"param,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

func WriteError(w http.ResponseWriter, status int, code, t, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResponse{
		Error: errorBody{
			Code:    code,
			Type:    t,
			Message: msg,
			Param:   nil,
		},
	}

	json.NewEncoder(w).Encode(resp)
}
