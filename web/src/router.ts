import type { Component } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'

import { ensureSettings } from '@/composables/useFleet'
import { NAV_ITEMS, type Section } from '@/nav'
import DisksView from '@/views/DisksView.vue'
import DevicesView from '@/views/DevicesView.vue'
import KeysView from '@/views/KeysView.vue'
import NetworksView from '@/views/NetworksView.vue'
import NodeDetailView from '@/views/NodeDetailView.vue'
import NodesView from '@/views/NodesView.vue'
import SettingsView from '@/views/SettingsView.vue'
import VMDetailView from '@/views/VMDetailView.vue'
import VMsView from '@/views/VMsView.vue'

const VIEWS: Record<Section, Component> = { nodes: NodesView, vms: VMsView, keys: KeysView, devices: DevicesView, networks: NetworksView, disks: DisksView, settings: SettingsView }

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    ...NAV_ITEMS.map((item) => ({ path: item.path, name: item.name, component: VIEWS[item.name] })),
    // `section` keeps the sidebar entry marked on a detail page.
    { path: '/nodes/:id', name: 'node', component: NodeDetailView, meta: { section: 'nodes' } },
    { path: '/vms/:id', name: 'vm', component: VMDetailView, meta: { section: 'vms' } },
    { path: '/:pathMatch(.*)*', redirect: '/nodes' },
  ],
})

router.beforeEach(async (to) => {
  if (to.name === 'settings') return true
  const s = await ensureSettings()
  return s && !s.setupComplete ? { name: 'settings' } : true
})
