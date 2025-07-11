// app/api/edit-webhook/route.ts
import { NextResponse } from 'next/server'

export async function GET(req: Request) {
    const backend = process.env.BACKEND_URL
    const { searchParams } = new URL(req.url)

    const id = searchParams.get('id')
    const type = searchParams.get('type')

    if (!id || !type) {
        return new Response('Missing id or type', { status: 400 })
    }

    const endpoint =
        type === 'validator'
            ? `${backend}/gnovalidator?id=${id}`
            : `${backend}/webhooksgovdao?id=${id}`

    const res = await fetch(endpoint)
    if (!res.ok) {
        const text = await res.text()
        return new Response(text, { status: res.status })
    }

    const data = await res.json()
    return NextResponse.json(data)
}

export async function PUT(req: Request) {
    const body = await req.json()

    const backend = process.env.BACKEND_URL

    const { target } = body
    if (!target || !["validator", "govdao"].includes(target)) {
        return new Response("Type invalide", { status: 400 })
    }

    const endpoint =
        target === "validator"
            ? `${process.env.BACKEND_URL}/gnovalidator`
            : `${process.env.BACKEND_URL}/webhooksgovdao`

    const res = await fetch(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    })

    if (!res.ok) {
        const txt = await res.text()
        return new Response(txt, { status: res.status })
    }

    return NextResponse.json({ success: true })
}
