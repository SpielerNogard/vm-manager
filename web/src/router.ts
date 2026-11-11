import type { Component } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'

import { NAV_ITEMS, type Section } from '@/nav'
import DevicesView from '@/views/DevicesView.vue'
import KeysView from '@/views/KeysView.vue'
import NodeDetailView from '@/views/NodeDetailView.vue'
import NodesView from '@/views/NodesView.vue'
import VMsView from '@/views/VMsView.vue'

const VIEWS: Record<Section, Component> = { nodes: NodesView, vms: VMsView, keys: KeysView, devices: DevicesView }

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    ...NAV_ITEMS.map((item) => ({ path: item.path, name: item.name, component: VIEWS[item.name] })),
    // `section` keeps the sidebar entry marked on a detail page.
    { path: '/nodes/:id', name: 'node', component: NodeDetailView, meta: { section: 'nodes' } },
    { path: '/:pathMatch(.*)*', redirect: '/nodes' },
  ],
})
