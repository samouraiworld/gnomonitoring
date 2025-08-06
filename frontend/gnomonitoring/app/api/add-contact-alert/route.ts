// app/api/add-contact-alert/route.ts

import { NextRequest, NextResponse } from 'next/server'

export async function POST(req: NextRequest) {
    const body = await req.json()
    const { user_id, moniker, namecontact, mention_tag, id_Webhook } = body
    console.log("(add)Saving contact with webhook ID:", id_Webhook);

    if (!user_id || !moniker || !namecontact || !mention_tag || !id_Webhook) {
        return new NextResponse("Missing parametre", { status: 400 });
    }

    try {
        const backendURL = process.env.BACKEND_URL
        const res = await fetch(`${backendURL}/alert-contacts`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ user_id, moniker, namecontact, mention_tag, id_Webhook }),
        })

        const data = await res.text()
        if (!res.ok) {
            return new NextResponse(data, { status: res.status })
        }

        return NextResponse.json({ success: true })
    } catch (err: any) {
        return new NextResponse(err.message || 'Error API', { status: 500 })
    }
}
