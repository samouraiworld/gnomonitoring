// app/page.tsx
'use client'

import { useEffect, useState } from 'react'
import Link from 'next/link'
import Image from 'next/image'
import { useRouter } from 'next/navigation'

type Webhook = {
  ID: number
  USER: string
  URL: string
  Type: string
  LastCheckedID: number
}
type Webhookvalidator = {
  ID: number
  USER: string
  URL: string
  Type: string
}

export default function HomePage() {
  const router = useRouter()
  const [govdaoWebhooks, setGovdaoWebhooks] = useState<Webhook[]>([])
  const [validatorWebhooks, setValidatorWebhooks] = useState<Webhookvalidator[]>([])

  // useEffect(() => {
  //   const backend = process.env.BACKEND_URL

  //   fetch(`${backend}/webhooksgovdao`)
  //     .then(res => res.json())
  //     .then(data => setGovdaoWebhooks(data))
  //     .catch(err => console.error('Erreur API GovDao:', err))

  //   fetch(`${backend}/gnovalidator`)
  //     .then(res => res.json())
  //     .then(data => setValidatorWebhooks(data))
  //     .catch(err => console.error('Erreur API Validator:', err))
  // }, [])

  useEffect(() => {
    fetch('/api/get-webhooks')
      .then(res => res.json())
      .then(data => {
        setGovdaoWebhooks(data.govdao)
        setValidatorWebhooks(data.validator)
      })
      .catch(err => console.error('Erreur API:', err))
  }, [])

  const handleDelete = async (id: number, type: 'govdao' | 'validator') => {
    const res = await fetch('/api/delete-webhook', {
      method: 'POST',
      body: JSON.stringify({ id, type }),
    })

    if (res.ok) {
      if (type === 'govdao') {
        setGovdaoWebhooks(prev => prev.filter(wh => wh.ID !== id))
      } else {
        setValidatorWebhooks(prev => prev.filter(wh => wh.ID !== id))
      }
    } else {
      alert("❌ Erreur lors de la suppression")
    }
  }

  return (
    <main className="min-h-screen bg-white text-black p-8">
      <div className="flex items-center gap-4 mb-6">
        <Image src="/gnoland.png" alt="Logo Gnomonitoring" width={40} height={40} />
        <h1 className="text-2xl font-bold">Gno Webhooks Manager</h1>
      </div>

      <div className="flex gap-6 mb-6">
        <Link href="/add?type=govdao" className="text-blue-600 underline">
          ➕ Add GovDao Webhook
        </Link>
        <Link href="/add?type=validator" className="text-blue-600 underline">
          ➕ Add Validator Webhook
        </Link>
      </div>

      <h2 className="text-xl font-semibold mt-8 mb-4">🛱️ Webhooks GovDao</h2>
      <table className="w-full border mb-12">
        <thead>
          <tr className="bg-gray-100">
            <th className="border px-4 py-2">ID</th>
            <th className="border px-4 py-2">USER</th>
            <th className="border px-4 py-2">Type</th>
            <th className="border px-4 py-2">URL</th>
            <th className="border px-4 py-2">Last Checked</th>
            <th className="border px-4 py-2">Actions</th>
          </tr>
        </thead>
        <tbody>
          {govdaoWebhooks.map((wh) => (
            <tr key={wh.ID} className="hover:bg-gray-50">
              <td className="border px-4 py-2">{wh.ID}</td>
              <td className="border px-4 py-2">{wh.USER}</td>
              <td className="border px-4 py-2">{wh.Type}</td>
              <td className="border px-4 py-2 text-xs">{wh.URL}</td>
              <td className="border px-4 py-2">{wh.LastCheckedID}</td>
              <td className="border px-4 py-2">
                <button
                  onClick={() => handleDelete(wh.ID, 'govdao')}
                  className="bg-red-600 hover:bg-red-700 text-white px-2 py-1 rounded text-sm"
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2 className="text-xl font-semibold mb-4">👮‍♂️ Webhooks GnoValidator</h2>
      <table className="w-full border">
        <thead>
          <tr className="bg-gray-100">
            <th className="border px-4 py-2">ID</th>
            <th className="border px-4 py-2">USER</th>
            <th className="border px-4 py-2">Type</th>
            <th className="border px-4 py-2">URL</th>
            <th className="border px-4 py-2">Actions</th>
          </tr>
        </thead>
        <tbody>
          {validatorWebhooks.map((wh) => (
            <tr key={wh.ID} className="hover:bg-gray-50">
              <td className="border px-4 py-2">{wh.ID}</td>
              <td className="border px-4 py-2">{wh.USER}</td>
              <td className="border px-4 py-2">{wh.Type}</td>
              <td className="border px-4 py-2 text-xs">{wh.URL}</td>
              <td className="border px-4 py-2">
                <button
                  onClick={() => handleDelete(wh.ID, 'validator')}
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
