// app/api/add-webhooks/route.ts

import { NextRequest, NextResponse } from 'next/server'

export async function POST(req: NextRequest) {
    const body = await req.json()
    const { UserID, Description, URL, Type, target } = body
    console.log("target :", target)
    const apiEndpoint =
        target === 'validator' ? 'validator' : 'govdao'
    console.log("Description:", Description, "Type:", Type);
    try {
        console.log('apiEndpoint', apiEndpoint)
        const backendURL = process.env.BACKEND_URL
        const res = await fetch(`${backendURL}/webhooks/${apiEndpoint}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ UserID, Description, URL, Type }),
        })

        const data = await res.text()
        if (!res.ok) {
            return new NextResponse(data, { status: res.status })
        }

        return NextResponse.json({ success: true })
    } catch (err: any) {
        return new NextResponse(err.message || 'Error backend', { status: 500 })
    }
}
