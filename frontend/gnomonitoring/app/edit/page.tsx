import { Suspense } from 'react'
import EditWebhook from './EditWebhook'

export default function EditPage() {
    return (
        <Suspense fallback={<p>Chargement…</p>}>
            <EditWebhook />
        </Suspense>
    )
}
