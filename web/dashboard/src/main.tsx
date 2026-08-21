import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router'

import '@mantine/core/styles.css'
import './styles.css'

import { Shell } from './components/Shell'
import { theme } from './theme'
import { BrowserBridgePage } from './pages/BrowserBridge'
import { Catalog } from './pages/Catalog'
import { ChannelDetail } from './pages/ChannelDetail'
import { Channels } from './pages/Channels'
import { Collections } from './pages/Collections'
import { Connections } from './pages/Connections'
import { Credentials } from './pages/Credentials'
import { Diagnostics } from './pages/Diagnostics'
import { NotFound } from './pages/NotFound'
import { Overview } from './pages/Overview'
import { RunDetail } from './pages/RunDetail'
import { Runs } from './pages/Runs'
import { SemanticProfiles } from './pages/SemanticProfiles'
import { ViewDetail } from './pages/ViewDetail'
import { Views } from './pages/Views'
import { Workbench } from './pages/Workbench'

/**
 * Reads go stale quickly: this is a local instance whose state the user changes
 * by hand and then expects to see. `retry: 1` because a loopback call that fails
 * twice is a real condition worth showing, not a blip worth hiding. Mutations
 * never retry — a write that may have landed must not be replayed blindly.
 */
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, refetchOnWindowFocus: true, retry: 1 },
    mutations: { retry: 0 },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <Shell>
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/workbench" element={<Workbench />} />
              <Route path="/channels" element={<Channels />} />
              <Route path="/channels/:id" element={<ChannelDetail />} />
              <Route path="/connections" element={<Connections />} />
              <Route path="/credentials" element={<Credentials />} />
              <Route path="/semantic-profiles" element={<SemanticProfiles />} />
              <Route path="/collections" element={<Collections />} />
              <Route path="/views" element={<Views />} />
              <Route path="/views/:id" element={<ViewDetail />} />
              <Route path="/runs" element={<Runs />} />
              <Route path="/runs/:id" element={<RunDetail />} />
              <Route path="/diagnostics" element={<Diagnostics />} />
              <Route path="/bridge" element={<BrowserBridgePage />} />
              <Route path="/catalog" element={<Catalog />} />
              <Route path="*" element={<NotFound />} />
            </Routes>
          </Shell>
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
)
