import { NextRequest, NextResponse } from 'next/server'

export async function POST(req: NextRequest) {
    const body = await req.json()
    const { user, url, type, target } = body

    const apiEndpoint =
        target === 'validator' ? 'gnovalidator' : 'webhooksgovdao'

    try {
        const backendURL = process.env.BACKEND_URL
        const res = await fetch(`${backendURL}/${apiEndpoint}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user, url, type }),
        })

        const data = await res.text()
        if (!res.ok) {
            return new NextResponse(data, { status: res.status })
        }

        return NextResponse.json({ success: true })
    } catch (err: any) {
        return new NextResponse(err.message || 'Erreur serveur', { status: 500 })
    }
}
