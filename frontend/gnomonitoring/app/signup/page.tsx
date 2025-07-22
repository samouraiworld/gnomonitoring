export default function SignUp() {
    return (
        <div className="max-w-md mx-auto mt-20">
            <h2 className="text-2xl font-semibold mb-4">Inscription</h2>
            <form className="space-y-4">
                <input type="text" placeholder="Nom" className="w-full border px-4 py-2 rounded" />
                <input type="email" placeholder="Email" className="w-full border px-4 py-2 rounded" />
                <input type="password" placeholder="Mot de passe" className="w-full border px-4 py-2 rounded" />
                <button className="bg-green-500 text-white px-4 py-2 rounded">Créer un compte</button>
            </form>
        </div>
    );
}
