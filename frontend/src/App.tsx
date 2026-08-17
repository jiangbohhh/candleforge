import { useState } from 'react'
import Market from './pages/Market'
import Backtest from './pages/Backtest'
import Trading from './pages/Trading'
import Strategies from './pages/Strategies'
import type { Tab } from './components/TopNav'

function App() {
  const [tab, setTab] = useState<Tab>('market')
  if (tab === 'market') return <Market onNav={setTab} />
  if (tab === 'backtest') return <Backtest onNav={setTab} />
  if (tab === 'strategies') return <Strategies onNav={setTab} />
  return <Trading onNav={setTab} />
}

export default App
