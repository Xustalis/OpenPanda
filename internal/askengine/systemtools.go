package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// The entry model otherwise has no clock of its own — it cannot answer "what
// time is it", "what day is today", or anything date-aware without a tool.
// time_now gives it the host's current time (design §7.3: the entry model
// runs on a host and may read its system clock). Underscored name: the
// Anthropic tools API restricts names to ^[a-zA-Z0-9_-]+$ and strict
// providers (DeepSeek /anthropic) 400 on dots.
func registerTimeTool(reg *entry.Registry) {
	reg.Register(entry.Tool{
		Name:        "time_now",
		Description: "获取宿主机当前系统时间（本地时区）。回答“现在几点/今天几号/星期几/明天是什么日期”等任何与当前时间有关的问题前，必须先调用此工具。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			now := time.Now()
			_, offset := now.Zone()
			sign := "+"
			if offset < 0 {
				sign, offset = "-", -offset
			}
			return fmt.Sprintf(
				"当前系统时间：%s\n日期：%s（%s）\n时区：%s (UTC%s%02d:%02d)\nUnix 时间戳：%d",
				now.Format("2006-01-02 15:04:05"),
				now.Format("2006-01-02"),
				weekdayCN(now),
				now.Location().String(), sign, offset/3600, (offset%3600)/60,
				now.Unix(),
			), nil
		},
	})
}

func weekdayCN(t time.Time) string {
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return names[int(t.Weekday())]
}

// weather tool endpoints (package vars so tests can point them at an
// httptest server). Open-Meteo is keyless, free, and HTTPS-only.
var (
	weatherGeocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	weatherForecastURL = "https://api.open-meteo.com/v1/forecast"
	weatherHTTPClient  = &http.Client{Timeout: 10 * time.Second}
)

// weatherDescription maps WMO weather codes to human-readable Chinese.
func weatherDescription(code int) string {
	switch {
	case code == 0:
		return "晴"
	case code <= 2:
		return "多云"
	case code == 3:
		return "阴"
	case code == 45, code == 48:
		return "雾"
	case code >= 51 && code <= 57:
		return "毛毛雨"
	case code >= 61 && code <= 67:
		return "雨"
	case code >= 71 && code <= 77:
		return "雪"
	case code >= 80 && code <= 82:
		return "阵雨"
	case code >= 85 && code <= 86:
		return "阵雪"
	case code >= 95 && code <= 99:
		return "雷雨"
	default:
		return "未知（代码 " + strconv.Itoa(code) + "）"
	}
}

type geoResult struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Country   string  `json:"country"`
	Timezone  string  `json:"timezone"`
}

type geocodeResponse struct {
	Results []geoResult `json:"results"`
}

type forecastResponse struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		Apparent    float64 `json:"apparent_temperature"`
		Humidity    int     `json:"relative_humidity_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Daily struct {
		Time        []string  `json:"time"`
		WeatherCode []int     `json:"weather_code"`
		TempMax     []float64 `json:"temperature_2m_max"`
		TempMin     []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

// registerWeatherTool imports weather.get (design §7.3's example controlled
// tool) backed by the keyless Open-Meteo API: geocode the location name,
// then fetch current conditions plus today/tomorrow.
func registerWeatherTool(reg *entry.Registry) {
	reg.Register(entry.Tool{
		Name:        "weather_get",
		Description: "查询指定地点的实时天气（含今天与明天的预报）。location 为城市名，支持中文或英文（如“北京”、“上海”、“Tokyo”）。",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string", "description": "城市名（中文或英文）"},
			},
			"required": []string{"location"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			loc, _ := args["location"].(string)
			loc = strings.TrimSpace(loc)
			if loc == "" {
				return "", fmt.Errorf("location 参数不能为空")
			}
			return fetchWeather(ctx, loc)
		},
	})
}

func fetchWeather(ctx context.Context, location string) (string, error) {
	geo, err := geocode(ctx, location)
	if err != nil {
		return "", err
	}

	fq, err := url.Parse(weatherForecastURL)
	if err != nil {
		return "", err
	}
	q := fq.Query()
	q.Set("latitude", fmt.Sprintf("%f", geo.Latitude))
	q.Set("longitude", fmt.Sprintf("%f", geo.Longitude))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min")
	q.Set("timezone", "auto")
	q.Set("forecast_days", "2")
	fq.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fq.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := weatherHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("天气服务不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("天气服务返回 %s", resp.Status)
	}
	var fc forecastResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&fc); err != nil {
		return "", fmt.Errorf("解析天气数据失败：%w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）当前天气：%s，气温 %.1f°C（体感 %.1f°C），湿度 %d%%，风速 %.1f km/h",
		geo.Name, geo.Country, weatherDescription(fc.Current.WeatherCode),
		fc.Current.Temperature, fc.Current.Apparent, fc.Current.Humidity, fc.Current.WindSpeed)
	for i, day := range fc.Daily.Time {
		if i >= 2 {
			break
		}
		fmt.Fprintf(&b, "\n%s：%s，%.1f°C / %.1f°C",
			day, weatherDescription(fc.Daily.WeatherCode[i]),
			fc.Daily.TempMax[i], fc.Daily.TempMin[i])
	}
	return b.String(), nil
}

func geocode(ctx context.Context, location string) (geoResult, error) {
	var out geocodeResponse
	gu, err := url.Parse(weatherGeocodeURL)
	if err != nil {
		return geoResult{}, err
	}
	q := gu.Query()
	q.Set("name", location)
	q.Set("count", "1")
	gu.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gu.String(), nil)
	if err != nil {
		return geoResult{}, err
	}
	resp, err := weatherHTTPClient.Do(req)
	if err != nil {
		return geoResult{}, fmt.Errorf("地理编码服务不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return geoResult{}, fmt.Errorf("地理编码服务返回 %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return geoResult{}, fmt.Errorf("解析地理编码失败：%w", err)
	}
	if len(out.Results) == 0 {
		return geoResult{}, fmt.Errorf("找不到地点“%s”，请换一个更明确的城市名", location)
	}
	return out.Results[0], nil
}
