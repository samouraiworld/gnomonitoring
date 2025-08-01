// app/api/add-contact-alert/route.ts

import { NextRequest, NextResponse } from 'next/server'

export async function POST(req: NextRequest) {
    const body = await req.json()
    const { user_id, moniker, namecontact, mention_tag } = body
    if (!user_id || !moniker || !namecontact || !mention_tag) {
        return new NextResponse("Paramètres manquants", { status: 400 });
    }

    try {
        const backendURL = process.env.BACKEND_URL
        const res = await fetch(`${backendURL}/alert-contacts`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user_id, moniker, namecontact, mention_tag }),
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
