<script lang="ts">
  import { api, type JobSummary, type Worker } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { fmtTime, STEP_NAMES } from '../lib/util'

  const LANES: [string, string, string][] = [
    ['download', '下載', '只用網路，最多同時 2 首'],
    ['process', 'AI 處理', '去人聲、對時、燒錄、檢查；交給 AI 伺服器'],
    ['system', '收尾', '佇列清空或曲庫有變動後：資料備份（data/）'],
  ]

  let logLines = $state<string[]>([])
  let logTitle = $state('')
  let logBox = $state<HTMLPreElement>()
  let loadedFor: string | null = null

  // 選了別的工作：重新載入它的紀錄
  $effect(() => {
    const id = store.selectedJob
    if (!id || id === loadedFor) return
    loadedFor = id
    logLines = []
    api<{ lines: string[]; job: JobSummary }>(`/jobs/${id}/log`).then((d) => {
      if (store.selectedJob !== id) return
      logLines = d.lines
      logTitle = d.job.title
      scroll(true)
    }).catch(() => {})
  })

  // 即時紀錄（SSE job.log）
  $effect(() =>
    store.on('job.log', (d: { id: string; lines: string[] }) => {
      if (d.id !== store.selectedJob || loadedFor !== d.id) return
      const atBottom = !logBox || logBox.scrollHeight - logBox.scrollTop - logBox.clientHeight < 40
      logLines = [...logLines, ...d.lines]
      scroll(atBottom)
    }),
  )

  function scroll(go: boolean) {
    if (go) queueMicrotask(() => logBox && (logBox.scrollTop = logBox.scrollHeight))
  }

  function logClass(line: string) {
    const msg = line.slice(10)
    if (/失敗|ERROR|\[x\]|\[!\]|Traceback/.test(msg)) return 'l-err'
    if (/^完成$/.test(msg)) return 'l-ok'
    return msg.startsWith(' ') ? '' : 'l-step'
  }

  function sub(j: JobSummary) {
    const steps = j.steps.map((s) => STEP_NAMES[s]).join(' → ')
    const mode = j.force ? '（強制重做）' : j.realign ? '（重新對時）' : ''
    const status =
      {
        queued: j.lane === 'process' && j.steps.includes('download') ? '已下載，等待 AI 處理' : '排隊中',
        running: j.cancelling ? '取消中…' : `${j.stage || '處理中'}…`,
        done: '完成',
        failed: `失敗：${j.error || ''}`,
        cancelled: '已取消',
      }[j.status] ?? j.status
    return `${steps}${mode} · ${status}`
  }

  async function cancel(j: JobSummary) {
    await store.attempt(() => api(`/jobs/${j.id}`, { method: 'DELETE' }), j.status === 'queued' ? '已移出佇列' : '取消中，會在下一個安全點停下')
  }

  function workerState(w: Worker) {
    if (!w.version_ok) return { cls: 'bad', text: `版本不同，請更新（${(w.version_diff ?? []).join('、')}）` }
    if (w.disabled) return { cls: '', text: '已停用' }
    if (!w.online) return { cls: '', text: `離線（最後連線 ${fmtTime(w.last_seen)}）` }
    if (w.tasks.length) {
      const t = w.tasks[0]
      const song = store.songs.get(t.song)
      const kind = { separate: '去人聲', align: '對時', align_from: 'AI 重對', align_line: 'AI 重對', qa: '檢查對時', render: '燒錄', reading: '假名' }[t.kind] ?? t.kind
      return { cls: 'online busy', text: `${kind}：${song?.title ?? t.song}` }
    }
    return { cls: 'online', text: '閒置' }
  }

  async function toggleWorker(w: Worker) {
    await store.attempt(() => api(`/workers/${encodeURIComponent(w.name)}`, { method: 'PATCH', body: { disabled: !w.disabled } }), w.disabled ? '已啟用' : '已停用（手上的任務會做完）')
  }
</script>

<aside class="panel activity">
  <div class="panel-head"><h2>AI 伺服器</h2></div>
  <div class="workers">
    {#each store.workers as w (w.name)}
      {@const st = workerState(w)}
      <div class="worker {st.cls}">
        <span class="dot"></span>
        <span class="name ellipsis" title={w.hardware.gpu ?? '沒有顯示卡'}>{w.name}{#if w.hardware.gpu}<span class="muted">　{w.hardware.gpu}</span>{/if}</span>
        <button type="button" class="view-link" onclick={() => toggleWorker(w)}>{w.disabled ? '啟用' : '停用'}</button>
        <span class="sub ellipsis">{st.text}</span>
      </div>
    {:else}
      <div class="empty">還沒有 AI 伺服器連線。在有顯示卡的電腦執行 worker（--nas 指到這台 NAS）。</div>
    {/each}
  </div>
  <div class="panel-head"><h2>處理佇列</h2></div>
  <div class="jobs">
    {#if !store.jobs.length}
      <div class="empty">目前沒有排隊中的處理</div>
    {/if}
    {#each LANES as [lane, name, tip]}
      {@const jobs = store.jobs.filter((j) => (j.lane || 'process') === lane)}
      {#if jobs.length}
        {@const active = jobs.filter((j) => j.status === 'queued' || j.status === 'running').length}
        <div class="lane-head" title={tip}><span>{name}</span><span class="muted">{active ? `${active} 件進行中` : '閒置'}</span></div>
        {#each jobs as j (j.id)}
          {@const cancellable = j.lane !== 'system' && (j.status === 'queued' || j.status === 'running') && !j.cancelling}
          <div
            role="button"
            tabindex="0"
            class="job {j.status}"
            class:selected={j.id === store.selectedJob}
            onclick={() => (store.selectedJob = j.id)}
            onkeydown={(e) => e.key === 'Enter' && (store.selectedJob = j.id)}
          >
            <span class="dot"></span>
            <span class="job-title ellipsis">{j.title || j.url}</span>
            <span class="job-time">{fmtTime(j.created)}</span>
            <span class="job-sub ellipsis" class:with-cancel={cancellable}>{sub(j)}</span>
            {#if cancellable}
              <button
                type="button"
                class="job-cancel"
                title={j.status === 'queued' ? '移出佇列' : '中斷處理'}
                onclick={(e) => {
                  e.stopPropagation()
                  cancel(j)
                }}>取消</button
              >
            {/if}
            {#if j.status === 'running'}
              <div class="bar" class:indeterminate={j.progress == null}>
                <span style={j.progress != null ? `width:${Math.round(j.progress * 100)}%` : undefined}></span>
              </div>
            {/if}
          </div>
        {/each}
      {/if}
    {/each}
  </div>
  <div class="panel-head log-head"><h2>執行紀錄</h2><span class="muted ellipsis">{logTitle}</span></div>
  <pre class="log" bind:this={logBox}>{#each logLines as line}<div class={logClass(line)}>{line}</div>{/each}</pre>
</aside>
