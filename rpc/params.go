package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

func HasParameter(params map[string]any, key string) bool {
	_, hasParam := params[key]
	return hasParam
}

func GetParameter(params map[string]any, key string) (any, error) {
	param, hasParam := params[key]
	if !hasParam {
		return nil, errors.New(fmt.Sprintf("Missing parameter '%s'.", key))
	}
	return param, nil
}

func GetBooleanParameter(params map[string]any, key string, failOnMissing bool, defaultValue bool) (bool, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramBoolean, paramIsBoolean := param.(bool)
	if !paramIsBoolean {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not of type boolean.", key))
	}
	return paramBoolean, nil
}

func GetStringParameter(params map[string]any, key string, failOnMissing bool, defaultValue string) (string, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramString, paramIsString := param.(string)
	if !paramIsString {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not of type string.", key))
	}
	return paramString, nil
}

func GetJSONNumberParameter(params map[string]any, key string, failOnMissing bool, defaultValue json.Number) (json.Number, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramString, paramIsString := param.(json.Number)
	if !paramIsString {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not a number type.", key))
	}
	return paramString, nil
}

func GetIntegerParameter(params map[string]any, key string, failOnMissing bool, defaultValue int64) (int64, error) {
	jsonNumber, err := GetJSONNumberParameter(params, key, failOnMissing, json.Number(strconv.FormatInt(defaultValue, 10)))
	if err != nil {
		return defaultValue, err
	}
	integer, err := jsonNumber.Int64()
	if err != nil {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' is a number not of type integer.", key))
	}

	return integer, nil
}

func GetFloatParameter(params map[string]any, key string, failOnMissing bool, defaultValue float64) (float64, error) {
	jsonNumber, err := GetJSONNumberParameter(params, key, failOnMissing, json.Number(strconv.FormatFloat(defaultValue, 'f', -1, 64)))
	if err != nil {
		return defaultValue, err
	}
	float, err := jsonNumber.Float64()
	if err != nil {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' is a JSON number not of type float.", key))
	}

	return float, nil
}

func GetStringArrayParameter(params map[string]any, key string, failOnMissing bool, defaultValue []string) ([]string, error) {
	param, err := GetParameter(params, key)
	if err != nil {
		if failOnMissing {
			return defaultValue, err
		}
		return defaultValue, nil
	}
	paramString, paramIsString := param.([]string)
	if !paramIsString {
		return defaultValue, errors.New(fmt.Sprintf("Parameter '%s' not of type string array.", key))
	}
	return paramString, nil
}
