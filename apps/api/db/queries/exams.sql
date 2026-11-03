-- name: ListPublishedExams :many
SELECT
    e.id,
    e.slug,
    ev.id AS version_id,
    ev.version,
    ev.title,
    ev.description,
    ev.skill,
    ev.duration_seconds,
    ev.published_at,
    count(q.id)::integer AS question_count
FROM exams e
JOIN exam_versions ev ON ev.exam_id = e.id
JOIN exam_sections es ON es.exam_version_id = ev.id
JOIN questions q ON q.section_id = es.id
WHERE ev.status = 'published'
  AND ev.purpose = 'mock'
  AND e.archived_at IS NULL
  -- Own uploads stay visible whatever happens to centres; everything else is
  -- public within its centre. The viewer's centre is read from the user id the
  -- query already takes, so no caller can pass the wrong one or omit it.
  AND (e.owner_user_id = sqlc.arg(user_id)
       OR (e.access_scope = 'public'
           AND (e.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM users WHERE id = sqlc.arg(user_id))
                OR (e.centre_id IS NULL AND EXISTS (
                      SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
                      WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))))
GROUP BY e.id, ev.id
ORDER BY ev.published_at DESC;

-- name: GetPublishedExam :one
SELECT
    e.id,
    e.slug,
    ev.id AS version_id,
    ev.version,
    ev.title,
    ev.description,
    ev.skill,
    ev.duration_seconds,
    ev.instructions,
    ev.published_at
FROM exams e
JOIN exam_versions ev ON ev.exam_id = e.id
WHERE e.id = $1
  AND ev.status = 'published'
  AND ev.purpose = 'mock'
  AND e.archived_at IS NULL
  -- Own uploads stay visible whatever happens to centres; everything else is
  -- public within its centre. The viewer's centre is read from the user id the
  -- query already takes, so no caller can pass the wrong one or omit it.
  AND (e.owner_user_id = sqlc.arg(user_id)
       OR (e.access_scope = 'public'
           AND (e.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM users WHERE id = sqlc.arg(user_id))
                OR (e.centre_id IS NULL AND EXISTS (
                      SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
                      WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))));

-- name: ListExamSections :many
SELECT id, exam_version_id, position, title, instructions, audio_url, passage, source_image_urls
FROM exam_sections
WHERE exam_version_id = $1
ORDER BY position;

-- name: ListSectionQuestions :many
SELECT id, section_id, position, type, prompt, points, config
FROM questions
WHERE section_id = $1
ORDER BY position;

-- name: ListQuestionOptions :many
SELECT id, question_id, position, label, value
FROM question_options
WHERE question_id = $1
ORDER BY position;

-- name: ListPracticeQuestions :many
SELECT
    q.id,
    q.position,
    q.type,
    q.prompt,
    q.points,
    q.config,
    es.id AS section_id,
    es.title AS section_title,
    es.instructions AS section_instructions,
    es.audio_url,
    es.passage,
    ev.title AS exam_title,
    ev.skill
FROM questions q
JOIN exam_sections es ON es.id = q.section_id
JOIN exam_versions ev ON ev.id = es.exam_version_id
JOIN exams e ON e.id = ev.exam_id
WHERE ev.status = 'published'
  AND e.archived_at IS NULL
  -- Own uploads stay visible whatever happens to centres; everything else is
  -- public within its centre. The viewer's centre is read from the user id the
  -- query already takes, so no caller can pass the wrong one or omit it.
  AND (e.owner_user_id = sqlc.arg(user_id)
       OR (e.access_scope = 'public'
           AND (e.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM users WHERE id = sqlc.arg(user_id))
                OR (e.centre_id IS NULL AND EXISTS (
                      SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
                      WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))))
  AND (sqlc.arg(skill)::text = '' OR ev.skill::text = sqlc.arg(skill)::text)
  AND (sqlc.arg(question_type)::text = '' OR q.type::text = sqlc.arg(question_type)::text)
ORDER BY random()
LIMIT sqlc.arg(result_limit);

-- name: GetPracticeAnswerKey :many
SELECT q.id, q.type, q.explanation, aa.value, aa.is_case_sensitive
FROM questions q
JOIN exam_sections es ON es.id = q.section_id
JOIN exam_versions ev ON ev.id = es.exam_version_id
JOIN exams e ON e.id = ev.exam_id
JOIN accepted_answers aa ON aa.question_id = q.id
WHERE q.id = $1
  AND ev.status = 'published'
  AND e.archived_at IS NULL
  -- Own uploads stay visible whatever happens to centres; everything else is
  -- public within its centre. The viewer's centre is read from the user id the
  -- query already takes, so no caller can pass the wrong one or omit it.
  AND (e.owner_user_id = sqlc.arg(user_id)
       OR (e.access_scope = 'public'
           AND (e.centre_id IS NOT DISTINCT FROM (SELECT centre_id FROM users WHERE id = sqlc.arg(user_id))
                OR (e.centre_id IS NULL AND EXISTS (
                      SELECT 1 FROM users vu JOIN centres vc ON vc.id = vu.centre_id
                      WHERE vu.id = sqlc.arg(user_id) AND vc.include_global)))))
ORDER BY aa.position;
