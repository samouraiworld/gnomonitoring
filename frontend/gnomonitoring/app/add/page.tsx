import { Suspense } from 'react'
import AddWebhookForm from './Form'

export default function AddPage() {
    return (
        <Suspense fallback={<div>Chargement du formulaire...</div>}>
            <AddWebhookForm />
        </Suspense>
    )
}