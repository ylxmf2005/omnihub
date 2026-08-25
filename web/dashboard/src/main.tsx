import { MantineProvider } from '@mantine/core'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter, Route, Routes } from 'react-router'

import '@mantine/core/styles.css'
import './styles.css'

import { Shell } from './components/Shell'
import { theme } from './theme'
import { ChannelDetail } from './pages/ChannelDetail'
import { Channels } from './pages/Channels'
import { NotFound } from './pages/NotFound'
import { RunDetail } from './pages/RunDetail'
import { Runs } from './pages/Runs'
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
              <Route path="/" element={<Workbench />} />
              <Route path="/sources" element={<Channels />} />
              <Route path="/sources/:id" element={<ChannelDetail />} />
              <Route path="/subscriptions" element={<Views />} />
              <Route path="/subscriptions/:id" element={<ViewDetail />} />
              <Route path="/activity" element={<Runs />} />
              <Route path="/activity/:id" element={<RunDetail />} />
              <Route path="*" element={<NotFound />} />
            </Routes>
          </Shell>
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
)
