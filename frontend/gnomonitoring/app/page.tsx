'use client'

import { useEffect, useState } from 'react'
import Link from 'next/link'
import Image from 'next/image'


type Webhook = {
  ID: number
  URL: string
  Type: string
  LastCheckedID: number
}

export default function HomePage() {
  const [webhooks, setWebhooks] = useState<Webhook[]>([])

  useEffect(() => {
    fetch('http://localhost:8080/webhooks')
      .then(res => res.json())
      .then(data => setWebhooks(data))
      .catch(err => console.error('Erreur API Go:', err))
  }, [])

  const deleteWebhook = async (id: number) => {
    const res = await fetch(`http://localhost:8080/webhooks?id=${id}`, {
      method: 'DELETE',
    })

    if (res.ok) {
      // Supprime localement dans le state
      setWebhooks((prev) => prev.filter((wh) => wh.ID !== id))
    } else {
      const text = await res.text()
      alert("❌ Error delete : " + text)
    }
  }

  return (
    <main className="min-h-screen bg-white text-black p-8">
      <div className="flex items-center gap-4 mb-6">
        <Image src="/gnoland.png" alt="Logo Gnomonitoring" width={40} height={40} />
        <h1 className="text-2xl font-bold">Webhooks GovDao</h1>
      </div>
      {/* <h1 className="text-2xl font-bold mb-4">📡 Webhooks GovDao</h1> */}

      <Link href="/add" className="text-blue-600 underline">
        <span>➕ Add Webhook</span>
      </Link>
      <table className="w-full border">
        <thead>
          <tr className="bg-gray-100">
            <th className="border px-4 py-2">ID</th>
            <th className="border px-4 py-2">Type</th>
            <th className="border px-4 py-2">URL</th>
            <th className="border px-4 py-2">Last Checked</th>
          </tr>
        </thead>
        <tbody>
          {webhooks.map((wh) => (
            <tr key={wh.ID} className="hover:bg-gray-50">
              <td className="border px-4 py-2">{wh.ID}</td>
              <td className="border px-4 py-2">{wh.Type}</td>
              <td className="border px-4 py-2 text-xs">{wh.URL}</td>
              <td className="border px-4 py-2">{wh.LastCheckedID}</td>
              <td className="border px-4 py-2">
                <button
                  onClick={() => deleteWebhook(wh.ID)}
                  className="bg-red-600 hover:bg-red-700 text-white px-2 py-1 rounded text-sm"
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </main>
  )
}
