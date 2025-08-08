import { Suspense } from 'react'
import AddWebhookForm from './Form'

export default function AddPage() {
    return (
        <Suspense fallback={<div>Loading form...</div>}>
            <AddWebhookForm />
        </Suspense>
    )
}