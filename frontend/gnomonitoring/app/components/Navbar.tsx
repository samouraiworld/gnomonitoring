export function Navbar() {
    return (
        <header className="bg-white shadow">
            <nav className="container mx-auto px-4 py-4 flex justify-between items-center">
                <a href="/" className="text-xl font-bold text-blue-600">GnoMonitoring</a>
                <div className="space-x-4">
                    <a href="/signin" className="text-gray-600 hover:text-black">Connexion</a>
                    <a href="/signup" className="text-gray-600 hover:text-black">Inscription</a>
                </div>
            </nav>
        </header>
    );
}
