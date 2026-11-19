package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// ListCommandsForOwner returns tool executions whose exploration belongs to one
// hosted-service owner. taskID is optional and is checked against the same owner.
func (d *DB) ListCommandsForOwner(ownerID string, taskID *int64, q string, page, size int) ([]CommandRecord, int, error) {
	if ownerID == "" {
		return nil, 0, nil
	}
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	where := `WHERE u.kind='tool_use' AND t.owner_id=$1 AND t.deleted_at IS NULL AND t.id IS NOT NULL`
	args := []any{ownerID}
	if taskID != nil {
		where += ` AND t.id=$2`
		args = append(args, *taskID)
	}
	argN := len(args) + 1
	if q != "" {
		where += fmt.Sprintf(` AND (u.tool ILIKE $%d OR u.detail ILIKE $%d)`, argN, argN)
		args = append(args, "%"+strings.TrimSpace(q)+"%")
		argN++
	}
	from := ` FROM activity u
JOIN tasks t ON t.exploration_id=u.exploration_id
LEFT JOIN activity r ON r.tool_use_id=u.tool_use_id AND r.kind='tool_result'`
	var total int
	if err := d.QueryRow(`SELECT COUNT(*)`+from+` `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(`SELECT u.id, u.exploration_id, COALESCE(u.worker,''), COALESCE(u.tool,''), COALESCE(u.detail,''),
COALESCE(r.detail,''), COALESCE(r.is_error,false), u.created_at`+from+` `+where+
		fmt.Sprintf(` ORDER BY u.id DESC LIMIT $%d OFFSET $%d`, argN, argN+1),
		append(args, size, page*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []CommandRecord{}
	for rows.Next() {
		var c CommandRecord
		if err := rows.Scan(&c.ID, &c.ExpID, &c.Worker, &c.Tool, &c.Command, &c.Output, &c.IsError, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// ToolStatsForOwner is the owner-scoped equivalent of ToolStats.
func (d *DB) ToolStatsForOwner(ownerID string, taskID *int64, q string) ([]ToolStat, error) {
	if ownerID == "" {
		return []ToolStat{}, nil
	}
	where := `WHERE u.kind='tool_use' AND t.owner_id=$1 AND t.deleted_at IS NULL`
	args := []any{ownerID}
	if taskID != nil {
		where += ` AND t.id=$2`
		args = append(args, *taskID)
	}
	argN := len(args) + 1
	if strings.TrimSpace(q) != "" {
		where += fmt.Sprintf(` AND (u.tool ILIKE $%d OR u.detail ILIKE $%d)`, argN, argN)
		args = append(args, "%"+strings.TrimSpace(q)+"%")
	}
	rows, err := d.Query(`
SELECT COALESCE(NULLIF(u.tool,''),'-'), COUNT(*),
       COUNT(*) FILTER (WHERE COALESCE(r.is_error,false))
FROM activity u
JOIN tasks t ON t.exploration_id=u.exploration_id
LEFT JOIN activity r ON r.tool_use_id=u.tool_use_id AND r.kind='tool_result'
`+where+`
GROUP BY 1 ORDER BY 2 DESC, 1 ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolStat{}
	for rows.Next() {
		var stat ToolStat
		if err := rows.Scan(&stat.Tool, &stat.Total, &stat.Errors); err != nil {
			return nil, err
		}
		out = append(out, stat)
	}
	return out, rows.Err()
}

func (d *DB) ListLLMRecordsForOwner(ownerID, model, session, task string, page, size int) ([]LLMRecord, int, error) {
	if ownerID == "" {
		return nil, 0, nil
	}
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	where := `WHERE EXISTS (
  SELECT 1 FROM tasks t
  WHERE t.owner_id=$1 AND t.deleted_at IS NULL AND t.id::text=COALESCE(lr.task_id,'')
)`
	args := []any{ownerID}
	argN := 2
	if model != "" {
		where += fmt.Sprintf(` AND lr.model=$%d`, argN)
		args = append(args, model)
		argN++
	}
	if session != "" {
		where += fmt.Sprintf(` AND lr.session_id ILIKE $%d`, argN)
		args = append(args, "%"+session+"%")
		argN++
	}
	if task != "" {
		where += fmt.Sprintf(` AND COALESCE(lr.task_id,'')=$%d`, argN)
		args = append(args, task)
		argN++
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records lr `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Query(`SELECT lr.id, lr.ts, COALESCE(lr.model,''), COALESCE(lr.profile_name,''),
COALESCE(lr.session_id,''), COALESCE(lr.task_id,''), COALESCE(lr.worker,''),
COALESCE(lr.latency_ms,0), COALESCE(lr.input_tokens,0), COALESCE(lr.output_tokens,0),
COALESCE(lr.cache_read,0), COALESCE(lr.cache_write,0), COALESCE(lr.status,''), COALESCE(lr.error,'')
FROM llm_records lr `+where+fmt.Sprintf(` ORDER BY lr.id DESC LIMIT $%d OFFSET $%d`, argN, argN+1),
		append(args, size, page*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LLMRecord{}
	for rows.Next() {
		var record LLMRecord
		if err := rows.Scan(&record.ID, &record.Ts, &record.Model, &record.ProfileName, &record.SessionID,
			&record.TaskID, &record.Worker, &record.LatencyMs, &record.InputTokens, &record.OutputTokens,
			&record.CacheRead, &record.CacheWrite, &record.Status, &record.Error); err != nil {
			return nil, 0, err
		}
		out = append(out, record)
	}
	return out, total, rows.Err()
}

func (d *DB) LLMTasksForOwner(ownerID string) ([]LLMTask, error) {
	if ownerID == "" {
		return []LLMTask{}, nil
	}
	rows, err := d.Query(`
SELECT lr.task_id, COUNT(*)
FROM llm_records lr
JOIN tasks t ON t.id::text=lr.task_id AND t.owner_id=$1 AND t.deleted_at IS NULL
WHERE COALESCE(lr.task_id,'')<>''
GROUP BY lr.task_id ORDER BY MAX(lr.id) DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LLMTask{}
	for rows.Next() {
		var task LLMTask
		if err := rows.Scan(&task.TaskID, &task.Count); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (d *DB) GetLLMRecordForOwner(id int64, ownerID string) (*LLMRecord, error) {
	var record LLMRecord
	var reqBody, respBody, rawReq, rawResp sql.NullString
	err := d.QueryRow(`
SELECT lr.id, lr.ts, COALESCE(lr.model,''), COALESCE(lr.profile_name,''),
COALESCE(lr.session_id,''), COALESCE(lr.task_id,''), COALESCE(lr.worker,''),
COALESCE(lr.latency_ms,0), COALESCE(lr.input_tokens,0), COALESCE(lr.output_tokens,0),
COALESCE(lr.cache_read,0), COALESCE(lr.cache_write,0), COALESCE(lr.status,''),
COALESCE(lr.error,''), lr.request_body, lr.response_body, lr.raw_request, lr.raw_response
FROM llm_records lr
JOIN tasks t ON t.id::text=COALESCE(lr.task_id,'')
WHERE lr.id=$1 AND t.owner_id=$2 AND t.deleted_at IS NULL`, id, ownerID).
		Scan(&record.ID, &record.Ts, &record.Model, &record.ProfileName, &record.SessionID, &record.TaskID,
			&record.Worker, &record.LatencyMs, &record.InputTokens, &record.OutputTokens, &record.CacheRead,
			&record.CacheWrite, &record.Status, &record.Error, &reqBody, &respBody, &rawReq, &rawResp)
	if err != nil {
		return nil, err
	}
	record.RequestBody = reqBody.String
	record.ResponseBody = respBody.String
	record.RawRequest = rawReq.String
	record.RawResponse = rawResp.String
	return &record, nil
}
