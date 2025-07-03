'use client'

import { useState, useEffect } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'

export default function AddWebhook() {
    const router = useRouter()
    const searchParams = useSearchParams()

    const typeParam = searchParams.get('type') || 'govdao' // valeur par défaut
    const apiEndpoint = typeParam === 'validator' ? 'gnovalidator' : 'webhookgovdao'

    const [user, setUser] = useState('')
    const [url, setURL] = useState('')
    const [type, setType] = useState('discord')
    const [message, setMessage] = useState('')

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault()

        const res = await fetch('/api/add-webhook', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ user, url, type, target: typeParam }),
        })

        if (res.ok) {
            setMessage('✅ Webhook ajouté avec succès !')
            setURL('')
            setType('discord')
            setTimeout(() => {
                router.push('/')
            }, 1000)
        } else {
            const txt = await res.text()
            setMessage(`❌ Erreur : ${txt}`)
        }
    }


    return (
        <main className="min-h-screen bg-white text-black p-8">
            <h1 className="text-2xl font-bold mb-4">
                ➕ Ajouter un Webhook {typeParam === 'validator' ? 'Validator' : 'GovDao'}
            </h1>

            <form onSubmit={handleSubmit} className="space-y-4 max-w-md">
                <div>
                    <label className="block font-medium">User</label>
                    <input
                        type="text"
                        value={user}
                        onChange={(e) => setUser(e.target.value)}
                        className="w-full border px-4 py-2 rounded"
                        required
                    />
                </div>
                <div>
                    <label className="block font-medium">URL</label>
                    <input
                        type="text"
                        value={url}
                        onChange={(e) => setURL(e.target.value)}
                        className="w-full border px-4 py-2 rounded"
                        required
                    />
                </div>

                <div>
                    <label className="block font-medium">Type</label>
                    <select
                        value={type}
                        onChange={(e) => setType(e.target.value)}
                        className="w-full border px-4 py-2 rounded"
                    >
                        <option value="discord">Discord</option>
                        <option value="slack">Slack</option>
                    </select>
                </div>

                <button
                    type="submit"
                    className="bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded"
                >
                    Ajouter
                </button>

                {message && <p className="mt-2">{message}</p>}
            </form>
        </main>
    )
}

