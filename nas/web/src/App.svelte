<script lang="ts">
  // 階段 0 的佔位畫面：確認前端打包、內嵌、和 NAS 伺服器連線都正常。功能在階段 3 實作。
  type Health = { ok: boolean; versions: Record<string, number> }

  let health = $state<Health | null>(null)
  let error = $state('')

  fetch('/healthz')
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
    .then((h: Health) => (health = h))
    .catch((e: Error) => (error = e.message))
</script>

<main>
  <h1>伴唱帶工作室</h1>
  {#if health}
    <p>NAS 伺服器連線正常</p>
    <ul>
      {#each Object.entries(health.versions) as [name, version]}
        <li><code>{name}</code> {version}</li>
      {/each}
    </ul>
  {:else if error}
    <p class="error">連不到 NAS 伺服器：{error}</p>
  {:else}
    <p>連線中…</p>
  {/if}
</main>
