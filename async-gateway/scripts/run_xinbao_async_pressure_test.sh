#!/usr/bin/env bash
set -euo pipefail

: "${XINBAO_API_KEY:?Set XINBAO_API_KEY first}"

BASE_URL="${BASE_URL:-https://async.xinbao-ai.com}"
MODEL="${MODEL:-gemini-3-pro-image-preview}"
COUNT="${COUNT:-20}"
POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-3}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-240}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
OUT_DIR="${OUT_DIR:-/tmp/xinbao-async-pressure-${RUN_ID}}"

mkdir -p "${OUT_DIR}"

submit_prompt() {
  local index="$1"
  local colors=(黄色 红色 蓝色 银色 绿色 橙色 黑色 白色 金色 紫色 青色 粉色)
  local subjects=(香蕉 苹果 马克杯 耳机 键盘 鼠标 手表 音箱 相机 台灯 背包 鞋子)
  local backgrounds=(白色 浅灰 米色 淡蓝 淡绿 奶油色)
  local styles=(棚拍 电商主图 极简广告 静物摄影 真实产品图 干净构图)
  local lights=(柔和自然光 高级棚拍光 柔光箱光效 清晰边缘光 质感侧光)

  local color="${colors[$(((index - 1) % ${#colors[@]}))]}"
  local subject="${subjects[$(((index - 1) % ${#subjects[@]}))]}"
  local background="${backgrounds[$(((index - 1) % ${#backgrounds[@]}))]}"
  local style="${styles[$(((index - 1) % ${#styles[@]}))]}"
  local light="${lights[$(((index - 1) % ${#lights[@]}))]}"

  printf '生成一张%s%s产品图，%s背景，单主体，%s，%s，细节清晰。' \
    "${color}" "${subject}" "${background}" "${style}" "${light}"
}

reference_urls_for_index() {
  local index="$1"
  local refs=(
    "https://d.uguu.se/ziYGQPeq.png"
    "https://h.uguu.se/VaRrXlhm.png"
    "https://h.uguu.se/QnzHMVXw.png"
    "https://n.uguu.se/BVNgVmly.png"
  )
  local first second third

  first="${refs[$(((index - 1) % ${#refs[@]}))]}"
  second="${refs[$((index % ${#refs[@]}))]}"
  third="${refs[$((((index + 1) % ${#refs[@]})))]}"

  jq -n --arg first "${first}" --arg second "${second}" --arg third "${third}" \
    '[$first, $second, $third]'
}

submit_one() {
  local index="$1"
  local task_dir="${OUT_DIR}/task-$(printf '%02d' "${index}")"
  local prompt
  local payload
  local refs_json
  local http_code
  local curl_exit=0
  local started_at ended_at

  mkdir -p "${task_dir}"
  prompt="$(submit_prompt "${index}")，请融合 3 张参考图的主体质感、配色和构图，生成一张新的商业产品海报。"
  refs_json="$(reference_urls_for_index "${index}")"
  printf '%s\n' "${prompt}" > "${task_dir}/prompt.txt"
  printf '%s\n' "${refs_json}" > "${task_dir}/reference-urls.json"

  payload="$(jq -n \
    --arg prompt "${prompt}" \
    --argjson refs "${refs_json}" \
    '{
      contents: [
        {
          role: "user",
          parts: (
            [{text: $prompt}] +
            ($refs | map({inlineData: {mimeType: "image/png", data: .}}))
          )
        }
      ],
      generationConfig: {
        responseModalities: ["IMAGE"],
        imageConfig: {output: "url"}
      }
    }')"
  printf '%s\n' "${payload}" > "${task_dir}/submit.request.json"

  started_at="$(date +%s)"
  if http_code="$(
    curl -sS -o "${task_dir}/submit.response.json" \
      -D "${task_dir}/submit.headers.txt" \
      -w '%{http_code}' \
      -X POST "${BASE_URL}/v1beta/models/${MODEL}:generateContent?output=url" \
      -H "Authorization: Bearer ${XINBAO_API_KEY}" \
      -H 'Content-Type: application/json' \
      --data "${payload}"
  )"; then
    curl_exit=0
  else
    curl_exit=$?
    http_code="000"
    printf 'submit curl failed: exit=%s\n' "${curl_exit}" > "${task_dir}/submit.error.txt"
    printf '{}\n' > "${task_dir}/submit.response.json"
  fi
  ended_at="$(date +%s)"

  jq -n \
    --argjson index "${index}" \
    --arg prompt "${prompt}" \
    --arg http_code "${http_code}" \
    --argjson curl_exit "${curl_exit}" \
    --arg started_at "${started_at}" \
    --arg ended_at "${ended_at}" \
    --arg task_id "$(jq -r '.id // ""' "${task_dir}/submit.response.json" 2>/dev/null)" \
    --arg status "$(jq -r '.status // ""' "${task_dir}/submit.response.json" 2>/dev/null)" \
    --slurpfile refs "${task_dir}/reference-urls.json" \
    --argjson created_at "$(jq -r '.created_at // 0' "${task_dir}/submit.response.json" 2>/dev/null)" \
    '{
      index: $index,
      prompt: $prompt,
      reference_urls: $refs[0],
      submit_http: $http_code,
      submit_curl_exit: $curl_exit,
      submit_started_at: ($started_at | tonumber),
      submit_ended_at: ($ended_at | tonumber),
      task_id: $task_id,
      submit_status: $status,
      created_at: $created_at
    }' > "${task_dir}/submit.meta.json"
}

poll_one() {
  local task_dir="$1"
  local poll_name="$2"
  local task_id="$3"
  local http_code curl_exit

  curl_exit=0
  if http_code="$(
    curl -sS -o "${task_dir}/${poll_name}.response.json" \
      -D "${task_dir}/${poll_name}.headers.txt" \
      -w '%{http_code}' \
      -H "Authorization: Bearer ${XINBAO_API_KEY}" \
      "${BASE_URL}/v1/tasks/${task_id}"
  )"; then
    curl_exit=0
  else
    curl_exit=$?
    http_code="000"
    printf 'poll curl failed: exit=%s\n' "${curl_exit}" > "${task_dir}/${poll_name}.error.txt"
    printf '{}\n' > "${task_dir}/${poll_name}.response.json"
  fi

  jq -n \
    --arg http_code "${http_code}" \
    --argjson curl_exit "${curl_exit}" \
    --arg status "$(jq -r '.status // ""' "${task_dir}/${poll_name}.response.json" 2>/dev/null)" \
    --arg error_code "$(jq -r '.error.code // ""' "${task_dir}/${poll_name}.response.json" 2>/dev/null)" \
    --arg retry_after "$(awk 'tolower($1)=="retry-after:"{gsub("\r","",$2); print $2}' "${task_dir}/${poll_name}.headers.txt" 2>/dev/null | head -n1)" \
    '{http_code:$http_code,curl_exit:$curl_exit,status:$status,error_code:$error_code,retry_after:$retry_after}' \
    > "${task_dir}/${poll_name}.meta.json"
}

fetch_content() {
  local task_dir="$1"
  local task_id="$2"
  local http_code curl_exit

  curl_exit=0
  if http_code="$(
    curl -sS \
      -o "${task_dir}/content.response.body" \
      -D "${task_dir}/content.headers.txt" \
      -w '%{http_code}' \
      -H "Authorization: Bearer ${XINBAO_API_KEY}" \
      "${BASE_URL}/v1/tasks/${task_id}/content"
  )"; then
    curl_exit=0
  else
    curl_exit=$?
  fi

  jq -n \
    --arg http_code "${http_code:-000}" \
    --argjson curl_exit "${curl_exit}" \
    --arg location "$(grep -i '^location:' "${task_dir}/content.headers.txt" 2>/dev/null | tr -d '\r' | sed 's/^location: //I' | head -n1)" \
    '{http_code:$http_code,curl_exit:$curl_exit,location:$location}' \
    > "${task_dir}/content.meta.json"
}

watch_one() {
  local task_dir="$1"
  local timeout_at now next_sleep attempt
  local task_id final_status retry_after finished_at first_url error_code
  local poll_name poll_response_path poll_meta_path
  local poll_http_000_count poll_http_200_count

  task_id="$(jq -r '.task_id' "${task_dir}/submit.meta.json")"
  if [[ -z "${task_id}" ]]; then
    jq -n '{final_status:"submit_failed"}' > "${task_dir}/final.meta.json"
    return
  fi

  timeout_at="$(( $(date +%s) + TIMEOUT_SECONDS ))"
  attempt=0
  final_status=""

  while :; do
    now="$(date +%s)"
    if (( now >= timeout_at )); then
      final_status="timeout"
      break
    fi

    attempt=$((attempt + 1))
    poll_name="poll-$(printf '%03d' "${attempt}")"
    poll_one "${task_dir}" "${poll_name}" "${task_id}"
    poll_response_path="${task_dir}/${poll_name}.response.json"
    poll_meta_path="${task_dir}/${poll_name}.meta.json"

    final_status="$(jq -r '.status // ""' "${poll_response_path}")"
    error_code="$(jq -r '.error.code // ""' "${poll_response_path}")"
    retry_after="$(jq -r '.retry_after // ""' "${poll_meta_path}")"

    if [[ "${final_status}" == "succeeded" || "${final_status}" == "failed" || "${final_status}" == "uncertain" ]]; then
      break
    fi

    if [[ "${retry_after}" =~ ^[0-9]+$ ]] && (( retry_after > POLL_INTERVAL_SECONDS )); then
      next_sleep="${retry_after}"
    else
      next_sleep="${POLL_INTERVAL_SECONDS}"
    fi
    sleep "${next_sleep}"
  done

  poll_name="poll-$(printf '%03d' "${attempt}")"
  poll_response_path="${task_dir}/${poll_name}.response.json"
  finished_at="$(jq -r '.finished_at // 0' "${poll_response_path}" 2>/dev/null)"
  first_url="$(jq -r '[.candidates[]?.content.parts[]?.inlineData.data][0] // ""' "${poll_response_path}" 2>/dev/null)"

  if [[ "${final_status}" == "succeeded" ]]; then
    fetch_content "${task_dir}" "${task_id}"
  else
    jq -n '{http_code:"",curl_exit:0,location:""}' > "${task_dir}/content.meta.json"
  fi

  poll_http_000_count="$(jq -s 'map(select(.http_code=="000")) | length' "${task_dir}"/poll-*.meta.json)"
  poll_http_200_count="$(jq -s 'map(select(.http_code=="200")) | length' "${task_dir}"/poll-*.meta.json)"

  jq -n \
    --arg task_id "${task_id}" \
    --arg final_status "${final_status}" \
    --arg error_code "${error_code}" \
    --argjson attempts "${attempt}" \
    --argjson finished_at "${finished_at:-0}" \
    --arg first_url "${first_url}" \
    --arg content_http "$(jq -r '.http_code // ""' "${task_dir}/content.meta.json")" \
    --arg content_location "$(jq -r '.location // ""' "${task_dir}/content.meta.json")" \
    --argjson poll_http_000_count "${poll_http_000_count}" \
    --argjson poll_http_200_count "${poll_http_200_count}" \
    --argjson local_finished_at "$(date +%s)" \
    '{
      task_id: $task_id,
      final_status: $final_status,
      error_code: $error_code,
      attempts: $attempts,
      finished_at: $finished_at,
      first_url: $first_url,
      content_http: $content_http,
      content_location: $content_location,
      poll_http_000_count: $poll_http_000_count,
      poll_http_200_count: $poll_http_200_count,
      local_finished_at: $local_finished_at
    }' > "${task_dir}/final.meta.json"
}

build_summary() {
  local summary_file="${OUT_DIR}/summary.json"
  jq -s '
    def to_num($v): ($v | tonumber? // 0);
    def duration:
      if (.submit.created_at > 0 and .final.finished_at > 0) then
        (.final.finished_at - .submit.created_at)
      else
        (.final.local_finished_at - .submit.submit_started_at)
      end;

    {
      generated_at: (now | todate),
      out_dir: $out_dir,
      config: {
        count: $count,
        model: $model,
        base_url: $base_url,
        poll_interval_seconds: $poll_interval,
        timeout_seconds: $timeout
      },
      totals: {
        requested: length,
        submit_202: map(select(.submit.submit_http == "202")) | length,
        submit_failed: map(select(.submit.task_id == "")) | length,
        succeeded: map(select(.final.final_status == "succeeded")) | length,
        failed: map(select(.final.final_status == "failed")) | length,
        uncertain: map(select(.final.final_status == "uncertain")) | length,
        timed_out: map(select(.final.final_status == "timeout")) | length,
        content_302: map(select(.final.content_http == "302")) | length
      },
      errors: {
        upstream_timeout: map(select(.final.error_code == "upstream_timeout")) | length,
        rate_limited: map(select(.final.error_code == "rate_limited")) | length
      },
      transport: {
        submit_http_000: map(select(.submit.submit_http == "000")) | length,
        poll_http_000: ([ .[] | .final.poll_http_000_count ] | add),
        poll_http_200: ([ .[] | .final.poll_http_200_count ] | add),
        tasks_with_poll_http_000: map(select(.final.poll_http_000_count > 0)) | length
      },
      timings: {
        succeeded_gateway_seconds: (
          [ .[] | select(.final.final_status == "succeeded") | duration ] as $durations
          | if ($durations | length) == 0 then
              {count:0,min:null,max:null,avg:null}
            else
              {
                count: ($durations | length),
                min: ($durations | min),
                max: ($durations | max),
                avg: (($durations | add) / ($durations | length))
              }
            end
        )
      },
      tasks: .
    }
  ' \
    --arg out_dir "${OUT_DIR}" \
    --argjson count "${COUNT}" \
    --arg model "${MODEL}" \
    --arg base_url "${BASE_URL}" \
    --argjson poll_interval "${POLL_INTERVAL_SECONDS}" \
    --argjson timeout "${TIMEOUT_SECONDS}" \
    "${OUT_DIR}"/task-*/combined.meta.json > "${summary_file}"

  jq -r '
    "generated_at=\(.generated_at)",
    "out_dir=\(.out_dir)",
    "submit_202=\(.totals.submit_202)/\(.totals.requested)",
    "succeeded=\(.totals.succeeded)/\(.totals.requested)",
    "failed=\(.totals.failed)",
    "uncertain=\(.totals.uncertain)",
    "timed_out=\(.totals.timed_out)",
    "content_302=\(.totals.content_302)",
    "upstream_timeout=\(.errors.upstream_timeout)",
    "rate_limited=\(.errors.rate_limited)",
    "submit_http_000=\(.transport.submit_http_000)",
    "poll_http_000=\(.transport.poll_http_000)",
    "tasks_with_poll_http_000=\(.transport.tasks_with_poll_http_000)",
    "gateway_duration_s_min=\(.timings.succeeded_gateway_seconds.min)",
    "gateway_duration_s_avg=\(.timings.succeeded_gateway_seconds.avg)",
    "gateway_duration_s_max=\(.timings.succeeded_gateway_seconds.max)"
  ' "${summary_file}" > "${OUT_DIR}/summary.txt"
}

echo "[pressure] out_dir=${OUT_DIR}"
echo "[pressure] submitting ${COUNT} tasks concurrently"

for index in $(seq 1 "${COUNT}"); do
  submit_one "${index}" &
done
wait

echo "[pressure] watching task completion"
for task_dir in "${OUT_DIR}"/task-*; do
  watch_one "${task_dir}" &
done
wait

for task_dir in "${OUT_DIR}"/task-*; do
  jq -n \
    --slurpfile submit "${task_dir}/submit.meta.json" \
    --slurpfile final "${task_dir}/final.meta.json" \
    '{submit:$submit[0],final:$final[0]}' > "${task_dir}/combined.meta.json"
done

build_summary

echo "[pressure] summary"
cat "${OUT_DIR}/summary.txt"
echo "[pressure] summary_json=${OUT_DIR}/summary.json"
