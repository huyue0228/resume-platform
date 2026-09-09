package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"resume-platform/internal/compat"
	pdftext "resume-platform/internal/pdftext"
)

type Object = map[string]any
type Field struct {
	Name, Column, Type, Relation string
	OnDelete                     string `json:"on_delete"`
	Timestamp                    string
	Null, Blank, Primary, Unique bool
	MaxLength                    *int `json:"max_length"`
	Default                      any
	Choices                      [][]any
}
type SerialField struct {
	Source, Type string
	ReadOnly     bool `json:"read_only"`
	Required     bool
	AllowNull    bool `json:"allow_null"`
}
type Resource struct {
	Table               string
	Fields              map[string]SerialField
	Permission          any
	PermissionsByAction map[string]any `json:"permissions_by_action"`
	ReadOnly            bool           `json:"read_only"`
}
type Spec struct {
	ExportFields    Object                  `json:"export_fields"`
	ImportSchemas   map[string]ImportSchema `json:"import_schemas"`
	Routes          []Route
	Models          map[string]struct{ Fields []Field }
	Resources       map[string]Resource
	PermissionTree  []Object            `json:"permission_tree"`
	RolePermissions map[string][]string `json:"role_permissions"`
	Configs         map[string]Object
	AIConfigs       map[string]Object `json:"ai_configs"`
}
type Config struct {
	DatabaseURL, RedisURL, Secret, MediaRoot, KernelURL, KernelToken, KernelBuild, Address string
	Debug                                                                                  bool
}
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}
type App struct {
	Extract func(context.Context, pdftext.Options) (pdftext.Result, error)
	Pool    *pgxpool.Pool
	Redis   *redis.Client
	Spec    Spec
	Config  Config
	HTTP    *http.Client
	Log     *slog.Logger
}
type apiError struct {
	Status int
	Detail any
}

func (e *apiError) Error() string { return fmt.Sprint(e.Detail) }
func bad(message string) error    { return &apiError{400, message} }
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func boolEnv(key string) bool {
	return strings.EqualFold(os.Getenv(key), "true") || os.Getenv(key) == "1"
}
func ConfigFromEnv() Config {
	database := os.Getenv("DATABASE_URL")
	if database == "" {
		u := &url.URL{Scheme: "postgres", User: url.UserPassword(env("POSTGRES_USER", "postgres"), env("POSTGRES_PASSWORD", "")), Host: env("POSTGRES_HOST", "db") + ":" + env("POSTGRES_PORT", "5432"), Path: env("POSTGRES_DB", "resume")}
		q := u.Query()
		q.Set("sslmode", env("POSTGRES_SSLMODE", "disable"))
		u.RawQuery = q.Encode()
		database = u.String()
	}
	return Config{DatabaseURL: database, RedisURL: env("REDIS_URL", env("CELERY_BROKER_URL", "redis://redis:6379/0")), Secret: env("PLATFORM_SECRET_KEY", os.Getenv("DJANGO_SECRET_KEY")), MediaRoot: env("MEDIA_ROOT", "/app/media"), KernelURL: env("AGENT_KERNEL_URL", "http://agent-kernel:8090"), KernelToken: os.Getenv("AGENT_KERNEL_TOKEN"), KernelBuild: os.Getenv("AGENT_KERNEL_BUILD"), Address: env("PLATFORM_ADDRESS", ":80"), Debug: boolEnv("DJANGO_DEBUG")}
}
func New(ctx context.Context, c Config) (*App, error) {
	if c.Secret == "" {
		return nil, errors.New("PLATFORM_SECRET_KEY is required")
	}
	pool, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	opt, err := redis.ParseURL(c.RedisURL)
	if err != nil {
		pool.Close()
		return nil, errors.New("invalid queue configuration")
	}
	a := &App{Pool: pool, Redis: redis.NewClient(opt), Config: c, HTTP: &http.Client{Timeout: 30 * time.Second}, Log: slog.Default()}
	if err := json.Unmarshal(compat.Spec, &a.Spec); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}
func (a *App) Close() { a.Pool.Close(); a.Redis.Close() }
func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("secure entropy unavailable")
	}
	return hex.EncodeToString(b)
}
func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func num(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	i, _ := strconv.ParseInt(str(v), 10, 64)
	return i
}
func truth(v any) bool { b, _ := v.(bool); return b }
func obj(v any) Object {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return Object{}
}
func list(v any) []any {
	if x, ok := v.([]any); ok {
		return x
	}
	return []any{}
}
func clone(m Object) Object    { b, _ := json.Marshal(m); var v Object; json.Unmarshal(b, &v); return v }
func quote(name string) string { return pgx.Identifier{name}.Sanitize() }
func rows(ctx context.Context, db DB, sql string, args ...any) ([]Object, error) {
	r, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []Object{}
	for r.Next() {
		var raw []byte
		if err := r.Scan(&raw); err != nil {
			return nil, err
		}
		var m Object
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, r.Err()
}
func one(ctx context.Context, db DB, sql string, args ...any) (Object, error) {
	values, err := rows(ctx, db, sql, args...)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, &apiError{404, "未找到记录"}
	}
	return values[0], nil
}
func (a *App) get(ctx context.Context, db DB, table string, id any) (Object, error) {
	if _, ok := a.Spec.Models[table]; !ok {
		return nil, bad("无效数据类型")
	}
	return one(ctx, db, "SELECT row_to_json(t) FROM "+quote(table)+" t WHERE "+quote(a.primaryColumn(table))+"=$1", id)
}
func (a *App) all(ctx context.Context, db DB, table string) ([]Object, error) {
	if _, ok := a.Spec.Models[table]; !ok {
		return nil, bad("无效数据类型")
	}
	return rows(ctx, db, "SELECT row_to_json(t) FROM "+quote(table)+" t ORDER BY "+quote(a.primaryColumn(table)))
}
func fieldFor(a *App, table, name string) (Field, bool) {
	for _, f := range a.Spec.Models[table].Fields {
		if name == f.Name || name == f.Column {
			return f, true
		}
	}
	return Field{}, false
}
func (a *App) save(ctx context.Context, db DB, table string, id any, values Object) (Object, error) {
	model, ok := a.Spec.Models[table]
	if !ok {
		return nil, bad("无效数据类型")
	}
	cols, marks, sets := []string{}, []string{}, []string{}
	args := []any{}
	for _, f := range model.Fields {
		if f.Primary {
			continue
		}
		v, present := values[f.Name]
		if !present {
			v, present = values[f.Column]
		}
		if f.Timestamp == "update" || (f.Timestamp == "create" && id == nil) {
			v = time.Now().UTC()
			present = true
		}
		if !present && id == nil {
			switch {
			case f.Default != nil:
				v = f.Default
				present = true
			case f.Null:
				v = nil
				present = true
			case f.Type == "CharField" || f.Type == "TextField" || f.Type == "EmailField":
				v = ""
				present = true
			}
		}
		if !present {
			continue
		}
		if (f.Type == "ForeignKey" || f.Type == "OneToOneField" || strings.Contains(f.Type, "Integer")) && v != nil {
			v = num(v)
		}
		if f.Type == "JSONField" {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, bad("JSON 字段无效")
			}
			v = string(b)
		}
		if f.Type == "DateField" && v == "" {
			v = nil
		}
		cols = append(cols, quote(f.Column))
		args = append(args, v)
		mark := "$" + strconv.Itoa(len(args))
		if f.Type == "JSONField" {
			mark += "::jsonb"
		}
		marks = append(marks, mark)
		sets = append(sets, quote(f.Column)+"="+mark)
	}
	if id != nil {
		if len(sets) == 0 {
			return a.get(ctx, db, table, id)
		}
		args = append(args, id)
		return one(ctx, db, "UPDATE "+quote(table)+" t SET "+strings.Join(sets, ",")+" WHERE id=$"+strconv.Itoa(len(args))+" RETURNING row_to_json(t)", args...)
	}
	return one(ctx, db, "INSERT INTO "+quote(table)+" AS t ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(marks, ",")+") RETURNING row_to_json(t)", args...)
}
func readBody(w http.ResponseWriter, r *http.Request) (Object, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	d := json.NewDecoder(r.Body)
	var body Object
	if d.Decode(&body) != nil || body == nil {
		return nil, bad("请求正文无效")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, bad("请求只能包含一个 JSON 对象")
	}
	return body, nil
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if status != 204 {
		json.NewEncoder(w).Encode(value)
	}
}
func (a *App) writeError(w http.ResponseWriter, err error) {
	var public *apiError
	if errors.As(err, &public) {
		if m, ok := public.Detail.(map[string]any); ok {
			write(w, public.Status, m)
		} else {
			write(w, public.Status, Object{"detail": public.Detail})
		}
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if pg.Code == "23505" {
			write(w, 400, Object{"detail": "数据重复，请检查唯一字段"})
			return
		}
		if pg.Code == "23503" {
			write(w, 409, Object{"detail": "关联数据已变化或仍被引用"})
			return
		}
	}
	a.Log.Error("request failed", "error_type", fmt.Sprintf("%T", err))
	write(w, 500, Object{"detail": "操作失败，请稍后重试"})
}

// PublicError 返回可展示的错误，不包含连接字符串、凭据或模型响应。
func PublicError(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "在途旧协议任务") {
		return "在途旧协议任务尚未结束，请先完成或取消后升级"
	}
	return publicMessage(err)
}

func (a *App) primaryColumn(table string) string {
	for _, f := range a.Spec.Models[table].Fields {
		if f.Primary {
			return f.Column
		}
	}
	return "id"
}
