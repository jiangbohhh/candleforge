import { useState } from 'react'
import Market from './pages/Market'
import Backtest from './pages/Backtest'
import type { Tab } from './components/TopNav'

function App() {
  const [tab, setTab] = useState<Tab>('market')
  return tab === 'market' ? <Market onNav={setTab} /> : <Backtest onNav={setTab} />
}

export default App
