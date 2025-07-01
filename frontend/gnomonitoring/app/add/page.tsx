'use client'

import { useState } from 'react'
import { useRouter } from 'next/navigation'

export default function AddWebhook() {
    const [url, setURL] = useState('')
    const [type, setType] = useState('discord')
    const [message, setMessage] = useState('')
    const router = useRouter()

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault()

        const res = await fetch('http://localhost:8080/webhook', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({ url, type }),
        })

        if (res.ok) {
            setMessage('✅ Webhook ajouté avec succès !')
            setURL('')
            setType('discord')
            // redirection vers la page d'accueil (facultatif)
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
            <h1 className="text-2xl font-bold mb-4">➕ Ajouter un Webhook</h1>

            <form onSubmit={handleSubmit} className="space-y-4 max-w-md">
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
