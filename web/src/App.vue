<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'

const nodes = ref([])
const vms = ref([])
const error = ref('')
const loadError = ref('')
const busy = ref(false)
const form = ref({ nodeId: '', name: '', cpus: 2, memoryMB: 2048, diskGB: 20, dataDisks: '', sshPublicKey: '' })

const onlineNodes = computed(() => nodes.value.filter((n) => n.online))
const host = window.location.hostname

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return null
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || res.statusText)
  return data
}

async function refresh() {
  try {
    ;[nodes.value, vms.value] = await Promise.all([api('GET', '/api/nodes'), api('GET', '/api/vms')])
    loadError.value = ''
    if (!form.value.nodeId && onlineNodes.value.length) form.value.nodeId = onlineNodes.value[0].id
  } catch (e) {
    loadError.value = `Hub nicht erreichbar: ${e.message}`
  }
}

async function createVM() {
  error.value = ''
  busy.value = true
  try {
    const f = form.value
    const dataDisksGB = f.dataDisks.split(',').map((s) => s.trim()).filter(Boolean).map(Number)
    await api('POST', '/api/vms', {
      nodeId: f.nodeId, name: f.name, cpus: f.cpus, memoryMB: f.memoryMB, diskGB: f.diskGB,
      dataDisksGB, sshPublicKey: f.sshPublicKey,
    })
    f.name = ''
    await refresh()
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}

async function deleteVM(vm) {
  if (!confirm(`VM "${vm.name}" löschen? Disks werden entfernt.`)) return
  try {
    await api('DELETE', `/api/vms/${vm.id}`)
    await refresh()
  } catch (e) {
    error.value = e.message
  }
}

const sshCommand = (vm) => `ssh -p ${vm.sshPort} ubuntu@${host}`
const copy = (text) => navigator.clipboard?.writeText(text)

let timer
onMounted(() => { refresh(); timer = setInterval(refresh, 2000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <main>
    <h1>vm-manager</h1>
    <p v-if="loadError" class="error">{{ loadError }}</p>

    <section>
      <h2>Nodes</h2>
      <table>
        <thead><tr><th>ID</th><th>Provider</th><th>Status</th><th>CPUs frei</th><th>RAM frei</th></tr></thead>
        <tbody>
          <tr v-for="n in nodes" :key="n.id">
            <td>{{ n.id }}</td>
            <td>{{ n.provider }}</td>
            <td><span :class="['badge', n.online ? 'running' : 'unknown']">{{ n.online ? 'online' : 'offline' }}</span></td>
            <td>{{ n.freeCpus }} / {{ n.cpus }}</td>
            <td>{{ n.freeMemoryMB }} / {{ n.memoryMB }} MB</td>
          </tr>
          <tr v-if="!nodes.length"><td colspan="5" class="muted">Keine Nodes verbunden.</td></tr>
        </tbody>
      </table>
    </section>

    <section>
      <h2>VMs</h2>
      <table>
        <thead><tr><th>Name</th><th>Node</th><th>Provider</th><th>Status</th><th>IP</th><th>SSH</th><th></th></tr></thead>
        <tbody>
          <tr v-for="vm in vms" :key="vm.id">
            <td>{{ vm.name }}</td>
            <td>{{ vm.nodeId }}</td>
            <td>{{ vm.provider }}</td>
            <td><span :class="['badge', vm.state]" :title="vm.error">{{ vm.state }}</span></td>
            <td>{{ vm.ip || '–' }}</td>
            <td>
              <template v-if="vm.sshPort">
                <code>{{ sshCommand(vm) }}</code>
                <button class="small" @click="copy(sshCommand(vm))">Kopieren</button>
              </template>
            </td>
            <td><button class="small danger" @click="deleteVM(vm)">Löschen</button></td>
          </tr>
          <tr v-if="!vms.length"><td colspan="7" class="muted">Noch keine VMs.</td></tr>
        </tbody>
      </table>
    </section>

    <section>
      <h2>Neue VM</h2>
      <form @submit.prevent="createVM">
        <label>Node
          <select v-model="form.nodeId" required>
            <option v-for="n in onlineNodes" :key="n.id" :value="n.id">{{ n.id }} ({{ n.provider }})</option>
          </select>
        </label>
        <label>Name <input v-model="form.name" required pattern="[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?" placeholder="dev-1" /></label>
        <label>CPUs <input v-model.number="form.cpus" type="number" min="1" required /></label>
        <label>RAM (MB) <input v-model.number="form.memoryMB" type="number" min="512" step="256" required /></label>
        <label>Disk (GB) <input v-model.number="form.diskGB" type="number" min="4" required /></label>
        <label>Daten-Disks (GB, kommasepariert) <input v-model="form.dataDisks" placeholder="z. B. 10, 20" /></label>
        <label class="wide">SSH Public Key <textarea v-model="form.sshPublicKey" rows="3" required placeholder="ssh-ed25519 AAAA…"></textarea></label>
        <div class="wide">
          <button type="submit" :disabled="busy || !onlineNodes.length">VM erstellen</button>
          <span v-if="!onlineNodes.length" class="muted"> Kein Node online.</span>
        </div>
        <p v-if="error" class="error wide">{{ error }}</p>
      </form>
    </section>
  </main>
</template>

<style>
:root {
  --bg: #f7f7f8; --fg: #1d1d1f; --muted: #6e6e73; --card: #fff; --border: #e3e3e6; --accent: #2f6fde; --danger: #c93a3a;
  font-family: -apple-system, system-ui, sans-serif; color: var(--fg); background: var(--bg);
}
@media (prefers-color-scheme: dark) {
  :root { --bg: #161618; --fg: #ececf0; --muted: #9a9aa2; --card: #1f1f23; --border: #333339; }
}
body { margin: 0; }
main { max-width: 1100px; margin: 0 auto; padding: 24px 16px; }
section { background: var(--card); border: 1px solid var(--border); border-radius: 8px; padding: 16px; margin-bottom: 16px; overflow-x: auto; }
h1 { font-size: 22px; } h2 { font-size: 16px; margin-top: 0; }
table { width: 100%; border-collapse: collapse; font-size: 14px; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--border); white-space: nowrap; }
th { color: var(--muted); font-weight: 500; }
code { font-size: 13px; }
.muted { color: var(--muted); }
.error { color: var(--danger); }
.badge { padding: 2px 8px; border-radius: 10px; font-size: 12px; background: var(--border); }
.badge.running { background: #d6f2dd; color: #1d6b32; }
.badge.error { background: #f8d8d8; color: #8e2020; }
.badge.starting, .badge.pending { background: #fdf0c8; color: #7a5a00; }
form { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
label { display: flex; flex-direction: column; gap: 4px; font-size: 13px; color: var(--muted); }
.wide { grid-column: 1 / -1; }
input, select, textarea { font: inherit; padding: 6px 8px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); color: var(--fg); }
button { font: inherit; padding: 6px 14px; border: 0; border-radius: 6px; background: var(--accent); color: #fff; cursor: pointer; }
button:disabled { opacity: .5; cursor: default; }
button.small { padding: 2px 8px; font-size: 12px; margin-left: 6px; }
button.danger { background: var(--danger); }
</style>
