// Mirrors the hub's JSON (internal/hub, internal/proto).

export interface Node {
  id: string
  hostname: string
  provider: string
  cpus: number
  memoryMB: number
  freeCpus: number
  freeMemoryMB: number
  state: 'pending' | 'online' | 'offline'
  connectedAt: string
  /** The node's config file without the hub token. */
  config?: Record<string, unknown>
  machine?: Machine
  chassis?: Chassis
  /** Set when the node registered with another hypervisor than it was created for. */
  error?: string
  /** Absent for nodes that report none (older than the update feature). */
  version?: string
  /** Installed by the installer, so the hub can update it. */
  updatable: boolean
  updateAvailable: boolean
  updating?: boolean
  /** Why the last update failed, with the end of the installer output. */
  updateError?: string
}

export interface Chassis {
  ip: string
  lastHandshake?: string
  registered: boolean
}

export interface Machine {
  os: string
  osVersion?: string
  arch: string
  cpuModel?: string
  cpus: number
  memoryMB?: number
}

export type VMState = 'pending' | 'starting' | 'running' | 'stopped' | 'error' | 'unknown'

export interface Attachment {
  diskId: string
  mountPath: string
  sizeGB: number
}

export interface DiskAttachment {
  vmId: string
  vmName: string
  nodeId: string
  mountPath: string
}

export interface StoredDisk {
  id: string
  name: string
  sizeGB: number
  allocatedBytes: number
  state: 'creating' | 'ready' | 'releasing' | 'missing'
  releasingOn?: string
  copyFrom?: string
  attachment?: DiskAttachment
  createdAt: string
}

export interface StorageFile {
  name: string
  sizeBytes: number
  allocatedBytes: number
}

export interface Storage {
  online: boolean
  endpoint: string
  version?: string
  orphans: StorageFile[]
}

export interface Disk {
  name: string
  file: string
  device: string
  readOnly: boolean
  sizeBytes: number
  allocatedBytes: number
  error?: string
}

export interface VM {
  id: string
  name: string
  cpus: number
  memoryMB: number
  diskGB: number
  attachments?: Attachment[]
  disks?: Disk[]
  sshPublicKeys: string[]
  nics?: NIC[]
  nodeId: string
  provider: string
  state: VMState
  power: 'running' | 'stopped'
  ip?: string
  error?: string
}

export interface NIC {
  network: string
  mac: string
  ip: string
  port: string
}

export interface Network {
  id: string
  name: string
  subnet: string
  builtin: boolean
  members: string[]
}

export interface Device {
  id: string
  name: string
  ip: string
  publicKey: string
  createdAt: string
  onHub?: boolean
}

export interface SSHKey {
  id: string
  name: string
  publicKey: string
  type: string
  fingerprint: string
  hub?: boolean
}

export interface Settings {
  listen: string
  token: string
  hub: { port: number }
  underlay: { port: number }
  clients: { endpoint: string; port: number }
  guestMTU: number
  storage: { endpoint?: string; port: number }
  setupComplete: boolean
  devices: number
  storageEndpoint: string
  vmsWithDisks: number
}

export interface Updates {
  version: string
  gateway?: string
  storage?: string
  latest?: string
  checkedAt?: string
  checkError?: string
  updateAvailable: boolean
  /** none: development build, no check. manual: the operator upgrades the stack. self: the hub updates itself. */
  hubUpdate: 'none' | 'manual' | 'self'
}

export type SettingsPatch = { token?: string; guestMTU?: number; clients?: { endpoint?: string }; storage?: { endpoint?: string } }

export interface CreateVMRequest {
  nodeId: string
  name: string
  cpus: number
  memoryMB: number
  diskGB: number
  disks: { diskId: string; mountPath: string }[]
  sshKeyIds: string[]
  networkIds?: string[]
}

async function text(path: string): Promise<string> {
  const res = await fetch(path)
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText)
  return res.text()
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || res.statusText)
  return data as T
}

export const api = {
  nodes: () => request<Node[]>('GET', '/api/nodes'),
  createNode: (id: string, provider: string) => request<Node>('POST', '/api/nodes', { id, provider }),
  deleteNode: (id: string) => request<void>('DELETE', `/api/nodes/${id}`),
  updateNode: (id: string) => request<void>('POST', `/api/nodes/${id}/update`),
  updates: () => request<Updates>('GET', '/api/updates'),
  checkUpdates: () => request<Updates>('POST', '/api/updates/check'),
  nodeConfig: (id: string, host?: string) =>
    text(`/api/nodes/${id}/config${host ? `?host=${encodeURIComponent(host)}` : ''}`),
  installCommand: (id: string, host?: string) =>
    text(`/api/nodes/${id}/install-command${host ? `?host=${encodeURIComponent(host)}` : ''}`),
  vms: () => request<VM[]>('GET', '/api/vms'),
  createVM: (req: CreateVMRequest) => request<VM>('POST', '/api/vms', req),
  deleteVM: (id: string) => request<void>('DELETE', `/api/vms/${id}`),
  startVM: (id: string) => request<VM>('POST', `/api/vms/${id}/start`),
  shutdownVM: (id: string) => request<VM>('POST', `/api/vms/${id}/shutdown`),
  stopVM: (id: string) => request<VM>('POST', `/api/vms/${id}/stop`),
  keys: () => request<SSHKey[]>('GET', '/api/keys'),
  addKey: (name: string, publicKey: string) => request<SSHKey>('POST', '/api/keys', { name, publicKey }),
  deleteKey: (id: string) => request<void>('DELETE', `/api/keys/${id}`),
  networks: () => request<Network[]>('GET', '/api/networks'),
  createNetwork: (req: { name: string; subnet?: string }) => request<Network>('POST', '/api/networks', req),
  deleteNetwork: (id: string) => request<void>('DELETE', `/api/networks/${id}`),
  setVMNetworks: (id: string, networkIds: string[], restartAllowed: boolean) =>
    request<VM>('PUT', `/api/vms/${id}/networks?restartAllowed=${restartAllowed}`, { networkIds }),
  disks: () => request<StoredDisk[]>('GET', '/api/disks'),
  createDisk: (name: string, sizeGB: number) => request<StoredDisk>('POST', '/api/disks', { name, sizeGB }),
  copyDisk: (id: string, name: string) =>
    request<{ disk: StoredDisk; warning?: string }>('POST', `/api/disks/${id}/copy`, { name }),
  growDisk: (id: string, sizeGB: number, restartAllowed: boolean) =>
    request<StoredDisk>('PATCH', `/api/disks/${id}?restartAllowed=${restartAllowed}`, { sizeGB }),
  deleteDisk: (id: string) => request<void>('DELETE', `/api/disks/${id}`),
  forceRelease: (id: string) => request<void>('POST', `/api/disks/${id}/release?force=true`),
  storage: () => request<Storage>('GET', '/api/storage'),
  setVMDisks: (id: string, disks: { diskId: string; mountPath: string }[], restartAllowed: boolean) =>
    request<VM>('PUT', `/api/vms/${id}/disks?restartAllowed=${restartAllowed}`, { disks }),
  devices: () => request<Device[]>('GET', '/api/devices'),
  addDevice: (name: string, onHub: boolean) =>
    request<{ device: Device; config: string }>('POST', '/api/devices', { name, onHub }),
  deleteDevice: (id: string) => request<void>('DELETE', `/api/devices/${id}`),
  settings: () => request<Settings>('GET', '/api/settings'),
  updateSettings: (patch: SettingsPatch) => request<Settings>('PUT', '/api/settings', patch),
  regenerateToken: () => request<Settings>('POST', '/api/settings/token'),
  deviceConfig: (id: string) => text(`/api/devices/${id}/config`),
}
