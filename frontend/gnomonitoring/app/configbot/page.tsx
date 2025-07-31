"use client";
import { ConfigBot } from "@/app/configbot/configbot";

export default function ConfigBotPage() {
    const {
        user,
        isLoaded,
        dailyHour,
        dailyMinute,
        setDailyHour,
        setDailyMinute,
        govWebhooks,
        valWebhooks,
        contacts,
        handleWebhookChange,
        handleUpdateNewWebhook,
        setGovWebhooks,
        setValWebhooks,
        setContacts,
        loadConfig,
        handleSaveNewWebhook,
        handleDeleteWebhook,
        handleCreateContact,
        handleDeleteContact,
        handleUpdateContact,
        handleAddContact,
        handleContactChange,
        handleSaveHour,
        handleAddWebhook,
    } = ConfigBot();

    if (!isLoaded || !user) return <p>Chargement...</p>;

    return (


        <div className="max-w-12xl mx-auto p-4 space-y-6">
            <h1 className="text-2xl font-bold">Configurer les alertes GnoMonitoring</h1>

            {/* User Info */}
            <section>
                <h2 className="text-xl font-semibold">Utilisateur</h2>
                <p>Email : {user?.primaryEmailAddress?.emailAddress}</p>
                <div className="flex space-x-2">
                    <label>Heure du rapport quotidien :</label>
                    <input
                        type="number"
                        value={dailyHour}
                        min={0}
                        max={23}
                        onChange={(e) => setDailyHour(Number(e.target.value))}
                        className="px-2 w-16"
                    />
                    <span>:</span>
                    <input
                        type="number"
                        value={dailyMinute}
                        min={0}
                        max={59}
                        onChange={(e) => setDailyMinute(Number(e.target.value))}
                        className="px-2 w-16"
                    />
                </div>
                <button
                    onClick={handleSaveHour}
                    className="bg-blue-600 text-white px-4 py-1 rounded"
                >
                    Save
                </button>
            </section>

            {/* Webhooks GovDAO */}
            <section>
                <h2 className="text-xl font-semibold mb-2">Webhooks GovDAO</h2>
                <table className="w-full table-auto ">
                    <thead className="bg-black-100 text-white">
                        <tr>
                            <th className="p-2 text-center">Description</th>
                            <th className="p-2 text-center">URL</th>
                            <th className="p-2 text-center">Type</th>
                            <th className="p-2 text-center">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        {govWebhooks.map((w, i) => (
                            <tr key={i} className="text-center">
                                <td className="p-2 text-center">
                                    <input
                                        type="text"
                                        value={w.DESCRIPTION ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "DESCRIPTION", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <input
                                        type="text-area"
                                        value={w.URL ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "URL", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <select
                                        value={w.Type ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "Type", e.target.value)
                                        }
                                        className="border p-1"
                                    >
                                        <option value="discord">Discord</option>
                                        <option value="slack">Slack</option>
                                    </select>
                                </td>
                                <td className="p-2 text-center space-x-2">
                                    <button
                                        onClick={() => handleSaveNewWebhook("gov", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Save
                                    </button>
                                    <button
                                        onClick={() => handleUpdateNewWebhook("gov", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Update
                                    </button>
                                    <button
                                        onClick={() => handleDeleteWebhook("gov", w.ID)}
                                        disabled={!w.ID}
                                        className={`px-2 py-1 rounded text-sm ${!w.ID ? "bg-gray-400" : "bg-red-500 text-white"}`}
                                    >
                                        Delete
                                    </button>
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
                <button
                    onClick={() => handleAddWebhook("gov")}
                    className="mt-2 text-blue-600 underline"
                >
                    + Ajouter un webhook GovDAO
                </button>
            </section>

            {/* Webhooks Validator */}
            <section>
                <h2 className="text-xl font-semibold mb-2">Webhooks Validator</h2>
                <table className="w-full table-auto ">
                    <thead className="bg-black-100 text-white text-center">
                        <tr>
                            <th className="p-2 text-center">DESCRIPTION</th>
                            <th className="p-2 text-center">URL</th>
                            <th className="p-2 text-center">Type</th>
                            <th className="p-2 text-center">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        {valWebhooks.map((w, i) => (
                            <tr key={i} className="text-center">
                                <td className="p-2">
                                    <input
                                        type="text"
                                        value={w.DESCRIPTION ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "DESCRIPTION", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2">
                                    <input
                                        type="text"
                                        value={w.URL ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "URL", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <select
                                        value={w.Type ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "Type", e.target.value)
                                        }
                                        className="border p-1"
                                    >
                                        <option value="discord">Discord</option>
                                        <option value="slack">Slack</option>
                                    </select>
                                </td>
                                <td className="p-2 text-center space-x-2">
                                    <button
                                        onClick={() => handleSaveNewWebhook("val", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Save
                                    </button>
                                    <button
                                        onClick={() => handleUpdateNewWebhook("val", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Update
                                    </button>
                                    <button
                                        onClick={() => handleDeleteWebhook("val", w.ID)}
                                        disabled={!w.ID}
                                        className={`px-2 py-1 rounded text-sm ${!w.ID ? "bg-gray-400" : "bg-red-500 text-white"}`}
                                    >
                                        Delete
                                    </button>
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
                <button
                    onClick={() => handleAddWebhook("val")}
                    className="mt-2 text-blue-600 underline"
                >
                    + Ajouter un webhook Validator
                </button>
            </section>

            {/* Alert Contacts */}
            <section>
                <h2 className="text-xl font-semibold">Contacts d'alerte</h2>
                {contacts.map((c, i) => (
                    <div key={i} className="flex gap-2 mb-2 items-center text-center">
                        <input
                            type="text"
                            placeholder="Moniker"
                            value={c.MONIKER}
                            onChange={(e) => handleContactChange(i, "MONIKER", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Nom du contact"
                            value={c.NAME}
                            onChange={(e) => handleContactChange(i, "NAME", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Tag de mention"
                            value={c.MENTIONTAG}
                            onChange={(e) => handleContactChange(i, "MENTIONTAG", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <div className="flex gap-1">
                            <button
                                onClick={() => handleCreateContact(i)}
                                className="bg-green-600 text-white px-2 rounded text-center"
                            >
                                Save
                            </button>
                            <button
                                onClick={() => handleUpdateContact(i)}
                                className="bg-green-600 text-white px-2 rounded text-center"
                            >
                                Update
                            </button>
                            <button
                                onClick={() => handleDeleteContact(c.ID)}
                                className="bg-red-600 text-white px-2 rounded text-center"
                            >
                                Delete
                            </button>
                        </div>
                    </div>
                ))}
                <button
                    onClick={handleAddContact}
                    className="text-blue-600 underline mt-2"
                >
                    + Ajouter un contact
                </button>
            </section>
        </div>
    );
}
